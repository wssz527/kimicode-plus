package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/redact"
)

const (
	APIPrefix       = "/enhance/v1"
	PackageVersion  = 1
	TrustUntrusted  = "untrusted-web"
	maxGoalBytes    = 20000
	maxDiffBytes    = 500000
	maxMessages     = 500
	maxAttachments  = 32
	maxMessageBytes = 100000
	maxAttachment   = 1 << 20
)

type Source struct {
	Site       string   `json:"site"`
	URL        string   `json:"url"`
	ThreadID   string   `json:"threadId,omitempty"`
	MessageIDs []string `json:"messageIds"`
}

type Message struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Role     string `json:"role"`
	Trust    string `json:"trust,omitempty"`
	Sequence int    `json:"sequence,omitempty"`
}

type Attachment struct {
	BlobRef    string `json:"blobRef,omitempty"`
	Name       string `json:"name"`
	MIME       string `json:"mime"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	DataBase64 string `json:"dataBase64,omitempty"`
	SourceURL  string `json:"sourceUrl,omitempty"`
	Status     string `json:"status"`
}

type TaskPackage struct {
	HostID           string             `json:"hostId,omitempty"`
	WebTarget        *desktop.WebTarget `json:"webTarget,omitempty"`
	Version          int                `json:"version"`
	TaskID           string             `json:"taskId"`
	Direction        string             `json:"direction"`
	SessionID        string             `json:"sessionId"`
	ProjectID        string             `json:"projectId"`
	Source           Source             `json:"source"`
	Goal             string             `json:"goal"`
	Messages         []Message          `json:"messages"`
	Diff             string             `json:"diff,omitempty"`
	Attachments      []Attachment       `json:"attachments"`
	Checkpoint       int                `json:"checkpoint"`
	ParentCheckpoint int                `json:"parentCheckpoint"`
	ParentHash       string             `json:"parentHash,omitempty"`
	LinkedTaskID     string             `json:"linkedTaskId,omitempty"`
	Mode             string             `json:"mode"`
}

type PreviewRequest struct {
	Task TaskPackage `json:"task"`
}

type PreviewResult struct {
	Task                 TaskPackage `json:"task"`
	Redacted             TaskPackage `json:"redacted"`
	Findings             []string    `json:"findings"`
	RequiresConfirmation bool        `json:"requiresConfirmation"`
	ConfirmationHash     string      `json:"confirmationHash"`
	PreviewID            string      `json:"previewId"`
	Warning              string      `json:"warning,omitempty"`
}

type SubmitRequest struct {
	Intent                string      `json:"intent"`
	Task                  TaskPackage `json:"task"`
	Confirmed             bool        `json:"confirmed"`
	ConfirmationHash      string      `json:"confirmationHash"`
	MockBehavior          string      `json:"mockBehavior,omitempty"`
	AcknowledgeUnverified bool        `json:"acknowledgeUnverifiedAttachments,omitempty"`
}

type ReceiptRecord struct {
	Attachments    []desktop.Blob `json:"attachments,omitempty"`
	Transport      string         `json:"transport"`
	ReceiptID      string         `json:"receiptId"`
	TaskID         string         `json:"taskId"`
	SessionID      string         `json:"sessionId"`
	Digest         string         `json:"digest"`
	Checkpoint     int            `json:"checkpoint"`
	State          string         `json:"state"`
	AcceptedAt     string         `json:"acceptedAt"`
	CheckpointHash string         `json:"checkpointHash,omitempty"`
}

type Record struct {
	ID                 string         `json:"id"`
	Intent             string         `json:"intent"`
	State              string         `json:"state"`
	Task               TaskPackage    `json:"task"`
	Receipt            *ReceiptRecord `json:"receipt,omitempty"`
	Findings           []string       `json:"findings"`
	ConfirmationHash   string         `json:"confirmationHash,omitempty"`
	CheckpointHash     string         `json:"checkpointHash,omitempty"`
	CheckpointAdvanced bool           `json:"checkpointAdvanced"`
	Note               string         `json:"note,omitempty"`
	Replayed           bool           `json:"replayed,omitempty"`
	CreatedAt          string         `json:"createdAt"`
	UpdatedAt          string         `json:"updatedAt"`
}

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func apiError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

var (
	hashPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	directionSet = map[string]bool{
		"CHAT_TO_CODE":          true,
		"CODE_TO_CHAT":          true,
		"CHAT_TO_CODE_CONTINUE": true,
	}
	modeSet = map[string]bool{"PLAN": true, "EXECUTE": true, "TOWER": true}
	roleSet = map[string]bool{"user": true, "assistant": true, "system": true}
)

func normalizeTask(task TaskPackage) (TaskPackage, error) {
	task.Source.MessageIDs = append([]string(nil), task.Source.MessageIDs...)
	task.Messages = append([]Message(nil), task.Messages...)
	task.Attachments = append([]Attachment(nil), task.Attachments...)
	task.TaskID = strings.TrimSpace(task.TaskID)
	task.SessionID = strings.TrimSpace(task.SessionID)
	task.ProjectID = strings.TrimSpace(task.ProjectID)
	task.Direction = strings.ToUpper(strings.TrimSpace(task.Direction))
	task.Mode = strings.ToUpper(strings.TrimSpace(task.Mode))
	task.ParentHash = strings.ToLower(strings.TrimSpace(task.ParentHash))
	task.LinkedTaskID = strings.TrimSpace(task.LinkedTaskID)
	task.Source.Site = strings.TrimSpace(task.Source.Site)
	task.Source.URL = strings.TrimSpace(task.Source.URL)
	task.Source.ThreadID = strings.TrimSpace(task.Source.ThreadID)
	if task.Source.MessageIDs == nil {
		task.Source.MessageIDs = []string{}
	}
	if task.Messages == nil {
		task.Messages = []Message{}
	}
	if task.Attachments == nil {
		task.Attachments = []Attachment{}
	}

	if task.Version != PackageVersion {
		return task, apiError(422, "UNSUPPORTED_VERSION", "任务包版本必须为 1")
	}
	if task.TaskID == "" || len(task.TaskID) > 128 {
		return task, apiError(422, "TASK_ID_INVALID", "taskId 必须存在且不超过 128 字符")
	}
	if !directionSet[task.Direction] {
		return task, apiError(422, "DIRECTION_INVALID", "direction 只能是 CHAT_TO_CODE、CODE_TO_CHAT 或 CHAT_TO_CODE_CONTINUE")
	}
	if task.SessionID == "" || len(task.SessionID) > 128 {
		return task, apiError(422, "SESSION_ID_INVALID", "sessionId 必须存在且不超过 128 字符")
	}
	if task.ProjectID == "" || len(task.ProjectID) > 128 {
		return task, apiError(422, "PROJECT_ID_INVALID", "projectId 必须存在且不超过 128 字符")
	}
	if !modeSet[task.Mode] {
		return task, apiError(422, "MODE_INVALID", "mode 只能是 PLAN、EXECUTE 或 TOWER")
	}
	if strings.TrimSpace(task.Goal) == "" || len(task.Goal) > maxGoalBytes {
		return task, apiError(422, "GOAL_INVALID", "goal 必须存在且不超过 20000 字节")
	}
	if task.Source.Site == "" {
		return task, apiError(422, "SOURCE_INVALID", "source.site 必须存在")
	}
	parsed, err := url.Parse(task.Source.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return task, apiError(422, "SOURCE_URL_INVALID", "source.url 必须是完整 HTTPS 地址；桥接不抓取该地址")
	}
	for i := range task.Source.MessageIDs {
		task.Source.MessageIDs[i] = strings.TrimSpace(task.Source.MessageIDs[i])
		if task.Source.MessageIDs[i] == "" {
			return task, apiError(422, "SOURCE_INVALID", "source.messageIds 不能包含空值")
		}
	}
	if len(task.Messages) > maxMessages {
		return task, apiError(422, "MESSAGES_INVALID", "消息数量超过上限")
	}
	if len(task.Messages) == 0 && (task.Direction == "CHAT_TO_CODE" || task.Direction == "CHAT_TO_CODE_CONTINUE") {
		return task, apiError(422, "MESSAGES_REQUIRED", "Chat 到 Code 的任务必须包含所选消息")
	}
	seen := make(map[string]bool, len(task.Messages))
	for i := range task.Messages {
		msg := &task.Messages[i]
		msg.ID = strings.TrimSpace(msg.ID)
		if msg.ID == "" || seen[msg.ID] {
			return task, apiError(422, "MESSAGE_ID_INVALID", "消息 ID 必须存在且唯一")
		}
		seen[msg.ID] = true
		if len(msg.Text) > maxMessageBytes {
			return task, apiError(422, "MESSAGE_TOO_LARGE", "单条消息超过大小上限")
		}
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if !roleSet[role] {
			return task, apiError(422, "MESSAGE_ROLE_INVALID", "消息 role 必须是 user、assistant 或 system")
		}
		msg.Role = role
		msg.Trust = TrustUntrusted
	}
	if len(task.Diff) > maxDiffBytes {
		return task, apiError(422, "DIFF_TOO_LARGE", "diff 超过大小上限")
	}
	if len(task.Attachments) > maxAttachments {
		return task, apiError(422, "ATTACHMENTS_INVALID", "附件数量超过上限")
	}
	for i := range task.Attachments {
		att := &task.Attachments[i]
		att.Name = strings.TrimSpace(att.Name)
		att.MIME = strings.TrimSpace(att.MIME)
		att.SHA256 = strings.ToLower(strings.TrimSpace(att.SHA256))
		att.SourceURL = strings.TrimSpace(att.SourceURL)
		if att.Name == "" {
			return task, apiError(422, "ATTACHMENT_INVALID", "附件必须有名称")
		}
		if att.Size < 0 || att.Size > maxAttachment {
			return task, apiError(422, "ATTACHMENT_INVALID", "附件大小无效或超过 1 MiB 上限")
		}
		if att.SHA256 != "" && !hashPattern.MatchString(att.SHA256) {
			return task, apiError(422, "ATTACHMENT_HASH_INVALID", "附件 sha256 格式无效")
		}
	}
	if task.Checkpoint < 1 {
		return task, apiError(422, "CHECKPOINT_INVALID", "checkpoint 必须大于等于 1")
	}
	if task.ParentCheckpoint < 0 {
		return task, apiError(422, "CHECKPOINT_INVALID", "parentCheckpoint 不能为负数")
	}
	if task.ParentCheckpoint > 0 && task.ParentHash == "" {
		return task, apiError(422, "PARENT_HASH_REQUIRED", "续接任务必须提供 parentHash")
	}
	if task.ParentHash != "" && !hashPattern.MatchString(task.ParentHash) {
		return task, apiError(422, "PARENT_HASH_INVALID", "parentHash 必须是 64 位小写十六进制")
	}
	if task.ParentCheckpoint > 0 && task.Checkpoint <= task.ParentCheckpoint {
		return task, apiError(422, "CHECKPOINT_ORDER", "checkpoint 必须大于 parentCheckpoint")
	}
	if task.ParentCheckpoint == 0 && task.ParentHash != "" {
		return task, apiError(422, "PARENT_HASH_UNEXPECTED", "没有父检查点时不应提供 parentHash")
	}
	if task.Direction == "CHAT_TO_CODE_CONTINUE" && task.ParentCheckpoint == 0 {
		return task, apiError(422, "PARENT_CHECKPOINT_REQUIRED", "CHAT_TO_CODE_CONTINUE 必须引用父检查点")
	}
	if err := assertOpaque(map[string]string{
		"taskId":       task.TaskID,
		"sessionId":    task.SessionID,
		"projectId":    task.ProjectID,
		"linkedTaskId": task.LinkedTaskID,
		"source.site":  task.Source.Site,
	}); err != nil {
		return task, err
	}
	for _, id := range task.Source.MessageIDs {
		if err := assertOpaque(map[string]string{"source.messageIds": id}); err != nil {
			return task, err
		}
	}
	selected := make(map[string]bool, len(task.Source.MessageIDs))
	for _, id := range task.Source.MessageIDs {
		selected[id] = true
	}
	for _, msg := range task.Messages {
		if err := assertOpaque(map[string]string{"messages[].id": msg.ID}); err != nil {
			return task, err
		}
		if (task.Direction == "CHAT_TO_CODE" || task.Direction == "CHAT_TO_CODE_CONTINUE") && !selected[msg.ID] {
			return task, apiError(422, "SOURCE_SELECTION_MISMATCH", "消息 "+msg.ID+" 不在 source.messageIds 所选范围内")
		}
	}
	if (task.Direction == "CHAT_TO_CODE" || task.Direction == "CHAT_TO_CODE_CONTINUE") && len(task.Source.MessageIDs) == 0 {
		return task, apiError(422, "SOURCE_SELECTION_REQUIRED", "Chat 到 Code 的任务必须声明所选消息 ID")
	}
	return task, nil
}

func assertOpaque(fields map[string]string) error {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if len(redact.Findings(fields[name])) > 0 {
			return apiError(422, "IDENTIFIER_SENSITIVE", name+" 看起来包含敏感内容；请使用不透明引用，不要写入秘密")
		}
	}
	return nil
}

func unverifiedAttachmentNames(attachments []Attachment) []string {
	var names []string
	for _, att := range attachments {
		if att.Status != "VERIFIED" && att.Status != "STORED" {
			names = append(names, att.Name)
		}
	}
	return names
}

func redactTask(task TaskPackage) (TaskPackage, []string) {
	out := task
	out.Goal = redact.Text(task.Goal)
	out.Diff = redact.Text(task.Diff)
	out.Source.URL = redact.Text(task.Source.URL)
	out.Source.ThreadID = redact.Text(task.Source.ThreadID)
	findings := redact.Findings(task.Goal + "\n" + task.Diff + "\n" + task.Source.URL + "\n" + task.Source.ThreadID)
	out.Messages = make([]Message, len(task.Messages))
	for i, msg := range task.Messages {
		copy := msg
		copy.Text = redact.Text(msg.Text)
		findings = append(findings, redact.Findings(msg.Text)...)
		out.Messages[i] = copy
	}
	out.Attachments = make([]Attachment, len(task.Attachments))
	for i, att := range task.Attachments {
		copy := att
		copy.DataBase64 = ""
		out.Attachments[i] = copy
	}
	return out, redact.JoinFindings(findings)
}

func canonicalTask(task TaskPackage) ([]byte, string, error) {
	raw, err := json.Marshal(task)
	if err != nil {
		return nil, "", fmt.Errorf("marshal task: %w", err)
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

func payloadDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func chainHash(taskID, sessionID string, checkpoint int, payloadHash string) string {
	sum := sha256.Sum256([]byte("bridge-checkpoint-v1\x00" + taskID + "\x00" + sessionID + "\x00" + strconv.Itoa(checkpoint) + "\x00" + payloadHash))
	return hex.EncodeToString(sum[:])
}
