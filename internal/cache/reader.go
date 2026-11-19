// Package cache reads Kimi Code session records and turns them into cache
// metrics. It is strictly read-only towards the official records: the only file
// this package writes is KimiCode+'s own SQLite database.
package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileRef identifies one wire.jsonl file. Inode and device pin the identity so a
// rotated or replaced file is detected even when size and mtime happen to match.
type FileRef struct {
	Path   string `json:"path"`
	Inode  uint64 `json:"inode"`
	Device uint64 `json:"device"`
	Offset int64  `json:"offset"`
}

// Step is one model step parsed from a step.end loop event.
//
// DecodeMS is llmServerDecodeMs, never llmStreamDurationMs - llmFirstTokenLatencyMs.
// On 160692 real samples llmFirstTokenLatencyMs exceeded llmStreamDurationMs in
// 109239 of them, so that subtraction goes negative; and
// llmStreamDurationMs == llmServerDecodeMs + llmClientConsumeMs held in 160690
// of them. See work/replan-20261116/keepalive-findings.md section 2.
type Step struct {
	SessionID string
	Agent     string
	StepUUID  string
	Model     string
	At        time.Time
	TurnID    string
	StepIndex int
	Finish    string

	InputOther         int64
	InputCacheRead     int64
	InputCacheCreation int64
	Output             int64

	FirstTokenMS int64
	DecodeMS     int64
}

// ModelUnknown marks a step whose usage.record could not be matched. It is never
// invented: a step that pairs with no record keeps this value.
const ModelUnknown = "UNKNOWN"

// used for the local decode speed guard; short steps make speed meaningless.
const (
	minSpeedOutput   = 20
	minSpeedDecodeMS = 300
)

var errShortRead = errors.New("incomplete line")

type usageCounts struct {
	InputOther         *int64 `json:"inputOther"`
	Output             *int64 `json:"output"`
	InputCacheRead     *int64 `json:"inputCacheRead"`
	InputCacheCreation *int64 `json:"inputCacheCreation"`
}

// empty reports whether a usage object carries no metrics. Real error and
// interrupted steps write `"usage": {}`; some writers spell the same thing as four
// explicit zeros. Both mean "nothing was consumed", so neither is stored and
// neither becomes an UNKNOWN model row.
func (u usageCounts) empty() bool {
	value := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	return value(u.InputOther) == 0 && value(u.Output) == 0 &&
		value(u.InputCacheRead) == 0 && value(u.InputCacheCreation) == 0
}

func (u usageCounts) key() string {
	value := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	return fmt.Sprintf("%d/%d/%d/%d", value(u.InputOther), value(u.Output), value(u.InputCacheRead), value(u.InputCacheCreation))
}

func (u usageCounts) step(step *Step) {
	set := func(dst *int64, src *int64) {
		if src != nil {
			*dst = *src
		}
	}
	set(&step.InputOther, u.InputOther)
	set(&step.Output, u.Output)
	set(&step.InputCacheRead, u.InputCacheRead)
	set(&step.InputCacheCreation, u.InputCacheCreation)
}

// wireLine is the union of the line shapes this package cares about. Every field
// is decoded defensively: the official writer may add fields freely, and an
// unknown line must never abort a scan.
type wireLine struct {
	Type  string `json:"type"`
	Time  int64  `json:"time"`
	Agent string `json:"agentId"`

	// usage.record
	Model      string      `json:"model"`
	Usage      usageCounts `json:"usage"`
	UsageScope string      `json:"usageScope"`

	// context.append_loop_event
	Event *struct {
		Type       string      `json:"type"`
		UUID       string      `json:"uuid"`
		TurnID     string      `json:"turnId"`
		Step       int         `json:"step"`
		FinishType string      `json:"finishReason"`
		Usage      usageCounts `json:"usage"`
		FirstToken *int64      `json:"llmFirstTokenLatencyMs"`
		ServerDec  *int64      `json:"llmServerDecodeMs"`
		// tool.call / tool.result identity, used to derive a blocking window from the
		// call lifecycle rather than from the delivery of a finished wait.
		Name       string `json:"name"`
		ToolCallID string `json:"toolCallId"`
	} `json:"event"`

	// task.started / task.terminated
	Info *struct {
		TaskID   string `json:"taskId"`
		Kind     string `json:"kind"`
		Status   string `json:"status"`
		Detached bool   `json:"detached"`
		Started  int64  `json:"startedAt"`
		Ended    *int64 `json:"endedAt"`
		AgentID  string `json:"agentId"`
	} `json:"info"`

	// turn.prompt
	Origin *struct {
		Kind string `json:"kind"`
	} `json:"origin"`

	// subagent.spawned / subagent.completed. This is the authoritative open/close pair
	// for a background subagent: task.started only appears once the subagent has been
	// registered, and a rejected Agent call produces no subagent events at all.
	SubagentID      string `json:"subagentId"`
	RunInBackground bool   `json:"runInBackground"`
	SubagentName    string `json:"subagentName"`
}

// pendingUsage is one usage.record waiting for its step.end.
type pendingUsage struct {
	model string
	usage usageCounts
}

// parser keeps the cross-line state needed to pair steps with models. It is
// per-file because pairing must not cross agent or session boundaries.
type parser struct {
	agent   string
	pending []pendingUsage
	steps   []Step

	// Background agent bookkeeping. Keyed by taskId; the open set is what makes
	// keepalive decide "a background subagent is still running".
	tasks map[string]taskState
	// OpenTasks is the number of detached agent tasks started and not yet
	// terminated as of the last parsed line.
	openTasks int
	// Blocked is true while a call to a structurally blocking tool (WaitFor) has been
	// issued and its result has not yet arrived. It is derived from the tool call
	// lifecycle, because no event is written when a wait begins: task.waitDelivered is
	// the delivery of a wait that has already finished.
	blocked bool
	// pendingBlockingCalls counts issued calls of a blocking tool awaiting a result.
	pendingBlockingCalls int
	// lastUserPrompt is the time of the newest turn.prompt written by the user.
	lastUserPrompt int64
	// backgroundSubagents holds the subagent IDs spawned with run_in_background that
	// have not completed yet.
	backgroundSubagents map[string]bool
}

type taskState struct {
	kind     string
	detached bool
}

func newParser(agent string) *parser {
	return &parser{agent: agent, tasks: map[string]taskState{}, backgroundSubagents: map[string]bool{}}
}

func (p *parser) consume(line []byte) error {
	var wire wireLine
	if err := json.Unmarshal(line, &wire); err != nil {
		// A malformed line is not fatal: the official writer may be mid-flush or
		// the file may contain a line from a newer protocol.
		return nil
	}
	switch wire.Type {
	case "usage.record":
		if wire.UsageScope != "" && wire.UsageScope != "turn" {
			return nil
		}
		if wire.Usage.empty() {
			return nil
		}
		model := wire.Model
		if model == "" {
			model = ModelUnknown
		}
		p.pending = append(p.pending, pendingUsage{model: model, usage: wire.Usage})
	case "context.append_loop_event":
		if wire.Event == nil {
			return nil
		}
		switch wire.Event.Type {
		case "tool.call":
			if isBlockingTool(wire.Event.Name) && wire.Event.ToolCallID != "" {
				p.pendingBlockingCalls++
				p.blocked = true
			}
			return nil
		case "tool.result":
			if wire.Event.ToolCallID != "" && p.pendingBlockingCalls > 0 {
				p.pendingBlockingCalls--
				p.blocked = p.pendingBlockingCalls > 0
			}
			return nil
		}
		if wire.Event.Type != "step.end" {
			return nil
		}
		if wire.Event.Usage.empty() {
			// error/interrupted steps carry no usage; there is nothing to store and
			// they must not be recorded as UNKNOWN.
			return nil
		}
		step := Step{
			Agent:        p.agent,
			StepUUID:     wire.Event.UUID,
			TurnID:       wire.Event.TurnID,
			StepIndex:    wire.Event.Step,
			Finish:       wire.Event.FinishType,
			At:           millis(wire.Time),
			Model:        ModelUnknown,
			DecodeMS:     -1,
			FirstTokenMS: -1,
		}
		if wire.Event.FirstToken != nil {
			step.FirstTokenMS = *wire.Event.FirstToken
		}
		if wire.Event.ServerDec != nil {
			step.DecodeMS = *wire.Event.ServerDec
		}
		wire.Event.Usage.step(&step)
		for index := len(p.pending) - 1; index >= 0; index-- {
			if p.pending[index].usage.key() != wire.Event.Usage.key() {
				continue
			}
			step.Model = p.pending[index].model
			p.pending = p.pending[index+1:]
			break
		}
		if step.StepUUID != "" {
			p.steps = append(p.steps, step)
		}
	case "subagent.spawned":
		if wire.SubagentID == "" || !wire.RunInBackground {
			return nil
		}
		p.backgroundSubagents[wire.SubagentID] = true
	case "subagent.completed":
		if wire.SubagentID == "" {
			return nil
		}
		delete(p.backgroundSubagents, wire.SubagentID)
	case "task.started":
		if wire.Info == nil || wire.Info.TaskID == "" {
			return nil
		}
		p.tasks[wire.Info.TaskID] = taskState{kind: wire.Info.Kind, detached: wire.Info.Detached}
		if wire.Info.Kind == "agent" && wire.Info.Detached {
			p.openTasks++
			if wire.Info.AgentID != "" {
				p.backgroundSubagents[wire.Info.AgentID] = true
			}
		}
	case "task.terminated":
		if wire.Info == nil {
			return nil
		}
		previous, ok := p.tasks[wire.Info.TaskID]
		if ok {
			delete(p.tasks, wire.Info.TaskID)
			if previous.kind == "agent" && previous.detached && p.openTasks > 0 {
				p.openTasks--
			}
			if wire.Info.AgentID != "" {
				delete(p.backgroundSubagents, wire.Info.AgentID)
			}
		}
		p.blocked = false
	case "task.waitDelivered":
		// NOT a blocking-start marker. Verified against a real record: the sequence is
		// WaitFor tool.call -> tool.result -> task.waitDelivered -> tool.result ->
		// llm.request. waitDelivered fires when the wait FINISHES and its result is
		// delivered, so treating it as "blocking started" inverts the meaning.
	case "turn.prompt":
		if wire.Origin != nil && wire.Origin.Kind == "user" {
			p.lastUserPrompt = wire.Time
		}
	}
	return nil
}

// blockingTools are the tools whose call keeps the main agent from producing another
// model request until they return. WaitFor is the structural case; the subagent tools
// block only when they are not run in the background, which the call arguments decide
// and which this reader cannot see, so they are deliberately not listed here.
func isBlockingTool(name string) bool {
	return name == "WaitFor"
}

func millis(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

// fileState carries the incremental read position and the parser state across
// scans, so an appended file is read once and a replaced file restarts cleanly.
type fileState struct {
	ref     FileRef
	parser  *parser
	modTime time.Time
	size    int64
}

// readIncremental reads new complete lines from path, starting at state's offset.
//
// Behaviour by case:
//   - new file            -> read from 0
//   - grown file          -> read from the stored offset
//   - truncated file      -> offset past size, restart from 0
//   - replaced file       -> inode/device changed, restart from 0
//   - incomplete last line-> kept in carry and completed on the next scan
func readIncremental(path string, state *fileState) ([]Step, bool, error) {
	handle, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return nil, false, err
	}
	inode, device, err := identityOf(handle, info)
	if err != nil {
		return nil, false, err
	}
	replaced := state.ref.Inode != 0 && (state.ref.Inode != inode || state.ref.Device != device)
	truncated := state.ref.Inode != 0 && !replaced && info.Size() < state.ref.Offset
	if replaced || truncated {
		state.parser = newParser(state.parser.agent)
		state.ref.Offset = 0
	}
	if state.ref.Inode == 0 {
		state.parser = newParser(state.parser.agent)
	}
	state.ref.Path = path
	state.ref.Inode = inode
	state.ref.Device = device
	state.modTime = info.ModTime()
	state.size = info.Size()
	if info.Size() <= state.ref.Offset {
		return nil, replaced || truncated, nil
	}

	if _, err = handle.Seek(state.ref.Offset, 0); err != nil {
		return nil, replaced || truncated, err
	}
	buffer := make([]byte, 1<<20)
	var pending []byte
	totalRead := int64(0)
	var steps []Step
	for {
		count, readErr := handle.Read(buffer)
		if count > 0 {
			totalRead += int64(count)
			chunk := buffer[:count]
			offset := 0
			if len(pending) > 0 {
				// A line that spanned the previous read is completed first.
				joined := make([]byte, 0, len(pending)+count)
				joined = append(joined, pending...)
				joined = append(joined, chunk...)
				chunk = joined
				pending = nil
			}
			for {
				relative := indexByte(chunk[offset:], '\n')
				if relative < 0 {
					break
				}
				end := offset + relative
				line := chunk[offset:end]
				offset = end + 1
				if len(line) > 0 {
					before := len(state.parser.steps)
					_ = state.parser.consume(line)
					steps = append(steps, state.parser.steps[before:]...)
				}
			}
			if offset < len(chunk) {
				// Keep the incomplete tail inside this call and keep reading. Official
				// records contain individual lines of several hundred kilobytes, so
				// stopping at the first buffer boundary would leave a large file only
				// partly ingested per scan.
				pending = append([]byte(nil), chunk[offset:]...)
			}
		}
		if readErr != nil {
			break
		}
		if count == 0 {
			break
		}
	}
	// The offset only advances past bytes that formed complete lines. A trailing
	// partial line is left unread so the next scan sees it together with whatever
	// the writer appended; nothing is buffered between scans, which is what keeps a
	// rewind from duplicating bytes.
	consumed := totalRead - int64(len(pending))
	if consumed < 0 {
		consumed = 0
	}
	state.ref.Offset += consumed
	state.parser.steps = state.parser.steps[:0]
	return steps, replaced || truncated, nil
}

func indexByte(data []byte, target byte) int {
	for index := 0; index < len(data); index++ {
		if data[index] == target {
			return index
		}
	}
	return -1
}

// SessionMeta is the subset of state.json this package uses.
type SessionMeta struct {
	ID        string `json:"id"`
	CWD       string `json:"cwd"`
	Archived  bool   `json:"archived"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	Agents    map[string]struct {
		Type string `json:"type"`
	} `json:"agents"`
}

func readSessionMeta(path string) (SessionMeta, error) {
	meta := SessionMeta{}
	data, err := os.ReadFile(path)
	if err != nil {
		return meta, err
	}
	if len(data) > 1<<20 {
		return meta, errors.New("state.json 超出预期大小")
	}
	if err = json.Unmarshal(data, &meta); err != nil {
		return meta, errors.New("state.json 无法解析")
	}
	return meta, nil
}

// sessionDir describes one discovered session on disk.
type sessionDir struct {
	ID      string
	Dir     string
	Meta    SessionMeta
	Agents  []string
	ModTime time.Time
}

// discover walks <home>/sessions/<bucket>/<session_id>/ and returns sessions
// newest-modified first, so a bounded scan covers the sessions a user is
// actually looking at.
func discover(home string) ([]sessionDir, error) {
	root := filepath.Join(home, "sessions")
	buckets, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	found := []sessionDir{}
	for _, bucket := range buckets {
		if !bucket.IsDir() {
			continue
		}
		bucketPath := filepath.Join(root, bucket.Name())
		sessions, readErr := os.ReadDir(bucketPath)
		if readErr != nil {
			continue
		}
		for _, session := range sessions {
			if !session.IsDir() || !strings.HasPrefix(session.Name(), "session_") {
				continue
			}
			dir := filepath.Join(bucketPath, session.Name())
			meta, metaErr := readSessionMeta(filepath.Join(dir, "state.json"))
			if metaErr != nil && !os.IsNotExist(metaErr) {
				continue
			}
			if meta.ID == "" {
				meta.ID = session.Name()
			}
			agents := []string{}
			if entries, agentErr := os.ReadDir(filepath.Join(dir, "agents")); agentErr == nil {
				for _, entry := range entries {
					if entry.IsDir() {
						agents = append(agents, entry.Name())
					}
				}
			}
			sort.Strings(agents)
			modTime := time.Time{}
			if info, statErr := os.Stat(filepath.Join(dir, "agents")); statErr == nil {
				modTime = info.ModTime()
			} else if info, statErr := os.Stat(filepath.Join(dir, "state.json")); statErr == nil {
				modTime = info.ModTime()
			}
			found = append(found, sessionDir{ID: meta.ID, Dir: dir, Meta: meta, Agents: agents, ModTime: modTime})
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].ModTime.Equal(found[j].ModTime) {
			return found[i].ID > found[j].ID
		}
		return found[i].ModTime.After(found[j].ModTime)
	})
	return found, nil
}
