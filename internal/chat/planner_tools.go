package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"enhancedkimi/internal/provider"
	"enhancedkimi/internal/security"
)

func plannerToolDefinitions(options PlannerOptions) []map[string]any {
	if !options.Tools {
		return nil
	}
	definitions := []map[string]any{
		plannerTool("git_status", "只读查看当前工作区的 Git 暂存、未暂存和未跟踪文件状态", map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}),
		plannerTool("list_files", "列出工作区内某个目录的文件和子目录", map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "description": "相对工作区的目录，默认 ."}}, "additionalProperties": false}),
		plannerTool("read_file", "读取工作区内的文本文件，最大 64 KiB", map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}, "additionalProperties": false}),
		plannerTool("search_files", "按正则表达式搜索工作区文本，返回文件、行号和内容", map[string]any{"type": "object", "properties": map[string]any{"pattern": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"}}, "required": []string{"pattern"}, "additionalProperties": false}),
	}
	if !options.ReadOnly {
		definitions = append(definitions, plannerTool("write_file", "在工作区写入文本文件；每次调用都需要用户明确批准", map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}, "required": []string{"path", "content"}, "additionalProperties": false}))
	}
	return definitions
}

func plannerTool(name, description string, parameters map[string]any) map[string]any {
	return map[string]any{"type": "function", "function": map[string]any{"name": name, "description": description, "parameters": parameters}}
}

func (p *PlannerService) executeTool(ctx context.Context, root string, state *plannerState, call provider.ToolCall, options PlannerOptions) (string, error) {
	var input struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
		Content string `json:"content"`
	}
	arguments := strings.TrimSpace(call.Function.Arguments)
	if arguments == "" {
		arguments = "{}"
	}
	if len(arguments) > 512<<10 || json.Unmarshal([]byte(arguments), &input) != nil {
		return "工具参数不是有效的 JSON 对象，请修正后重试。", nil
	}
	if !options.Tools {
		return "当前规划未开放工具，调用已被拒绝。", nil
	}
	if call.Function.Name != "write_file" {
		result, err := plannerReadOnlyTool(ctx, root, call.Function.Name, input.Path, input.Pattern)
		if err != nil {
			return "工具未执行：" + err.Error(), nil
		}
		return result, ctx.Err()
	}
	if options.ReadOnly {
		return "当前规划预设是只读模式，写入已被拒绝。", nil
	}
	if len(input.Content) > 256<<10 {
		return "写入内容超过 256 KiB，未执行。", nil
	}
	if _, err := plannerPath(root, input.Path, true); err != nil {
		return "写入已拒绝：" + err.Error(), nil
	}
	approval := &PlannerApproval{ID: security.Random()[:24], Title: "write_file · " + input.Path, Detail: boundedPlannerText(arguments, 8000)}
	decision := make(chan bool, 1)
	state.mu.Lock()
	if state.cancel == nil {
		state.mu.Unlock()
		return "", context.Canceled
	}
	state.approve = decision
	state.thread.Approval = approval
	if err := p.save(ctx, &state.thread); err != nil {
		state.mu.Unlock()
		return "", err
	}
	state.mu.Unlock()
	p.bus.Publish("planner:"+state.thread.ID, p.snapshot(state))
	allowed := false
	select {
	case allowed = <-decision:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	state.mu.Lock()
	state.approve = nil
	state.thread.Approval = nil
	state.mu.Unlock()
	if !allowed {
		return "用户拒绝了这次写入，未执行。", nil
	}
	if err := ctx.Err(); err != nil {
		return "工具调用已取消，未执行。", err
	}
	// Re-validate and then write through an os.Root: the kernel-level root
	// constraint is the actual guarantee, so a symlink created between the check
	// and the write still cannot escape the workspace. Nothing below races a
	// concurrent attacker; it only removes the check-then-use gap.
	if err := plannerWriteWithinRoot(root, input.Path, []byte(input.Content)); err != nil {
		return "写入失败：" + err.Error(), nil
	}
	return fmt.Sprintf("已按用户批准写入 %s（%d 字节）。", input.Path, len(input.Content)), ctx.Err()
}

func (p *PlannerService) approveTool(state *plannerState, approvalID string, allow bool) (PlannerThread, error) {
	state.mu.Lock()
	approval := state.thread.Approval
	decision := state.approve
	if approval == nil || decision == nil || approvalID != "" && approval.ID != approvalID {
		state.mu.Unlock()
		return PlannerThread{}, errors.New("没有匹配的待审批工具，请刷新规划线程")
	}
	state.thread.Approval = nil
	if err := p.save(context.Background(), &state.thread); err != nil {
		state.mu.Unlock()
		return PlannerThread{}, err
	}
	thread := plannerSnapshot(state)
	state.mu.Unlock()
	decision <- allow
	p.bus.Publish("planner:"+thread.ID, thread)
	return thread, nil
}

func plannerPath(root, requested string, allowMissing bool) (string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("工作区不可用")
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = "."
	}
	if strings.ContainsRune(requested, '\x00') {
		return "", errors.New("路径无效")
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(resolvedRoot, candidate)
	}
	candidate = filepath.Clean(candidate)
	relative, err := filepath.Rel(resolvedRoot, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("路径超出当前工作区")
	}
	if err := plannerRejectSymlinkSegments(resolvedRoot, candidate); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if !allowMissing || !errors.Is(err, fs.ErrNotExist) {
			return "", errors.New("路径不存在或无法访问")
		}
		resolved, err = plannerResolveMissing(resolvedRoot, candidate)
		if err != nil {
			return "", err
		}
	}
	relative, err = filepath.Rel(resolvedRoot, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("路径经符号链接超出当前工作区")
	}
	if info, statErr := os.Stat(resolved); statErr == nil && !info.Mode().IsRegular() && !info.IsDir() {
		return "", errors.New("只允许普通文件或目录")
	}
	return resolved, nil
}

// plannerRejectSymlinkSegments walks every existing path segment between the
// workspace root and candidate. A symlink anywhere on that path could otherwise
// redirect a missing target outside the workspace after it is created.
func plannerRejectSymlinkSegments(root, candidate string) error {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return errors.New("路径超出当前工作区")
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, fs.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return errors.New("路径不存在或无法访问")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("路径包含符号链接，已拒绝")
		}
	}
	return nil
}

// plannerResolveMissing resolves the nearest existing ancestor and appends the
// still-missing segments, so docs/sub/plan.md can be created without requiring
// every parent to exist before approval.
func plannerResolveMissing(root, candidate string) (string, error) {
	missing := []string{}
	current := candidate
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("路径包含符号链接，已拒绝")
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", errors.New("路径不存在或无法访问")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("父目录不存在或无法访问")
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", errors.New("父目录不存在或无法访问")
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("路径经符号链接超出当前工作区")
	}
	parts := append([]string{resolved}, missing...)
	return filepath.Join(parts...), nil
}

// plannerWriteWithinRoot performs the approved write through an os.Root. Every
// path keeps being validated by plannerPath first (clear error messages, symlink
// rejection), but the write itself is bound to the workspace root by the kernel,
// so the check and the write cannot disagree.
func plannerWriteWithinRoot(root, requested string, data []byte) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return errors.New("工作区不可用")
	}
	path, err := plannerPath(resolvedRoot, requested, true)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(resolvedRoot, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("路径超出当前工作区")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	// Re-validate after creating the parent directories: mkdir is the only step
	// that could have observed a new component.
	path, err = plannerPath(resolvedRoot, requested, true)
	if err != nil {
		return err
	}
	relative, err = filepath.Rel(resolvedRoot, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("路径超出当前工作区")
	}
	workspace, err := os.OpenRoot(resolvedRoot)
	if err != nil {
		return errors.New("工作区不可用")
	}
	defer workspace.Close()
	if info, err := workspace.Lstat(relative); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("目标路径是符号链接，已拒绝")
	}
	return workspace.WriteFile(relative, data, 0644)
}

func plannerReadOnlyTool(ctx context.Context, root, name, path, pattern string) (string, error) {
	switch name {
	case "git_status":
		return plannerGitStatus(ctx, root)
	case "list_files":
		return plannerList(root, path)
	case "read_file":
		return plannerRead(root, path)
	case "search_files":
		return plannerSearch(root, path, pattern)
	default:
		return "", errors.New("不支持的工具 " + name)
	}
}

type plannerGitOutput struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (output *plannerGitOutput) Write(data []byte) (int, error) {
	n := min(len(data), output.limit-output.Len())
	_, _ = output.Buffer.Write(data[:n])
	output.truncated = output.truncated || n < len(data)
	return len(data), nil
}

func plannerGitStatus(ctx context.Context, root string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	env := []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0"}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
			env = append(env, entry)
		}
	}
	run := func(limit int, args ...string) (string, bool, error) {
		args = append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.DevNull}, args...)
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		cmd.Env = env
		out := plannerGitOutput{limit: limit}
		cmd.Stdout = &out
		err := cmd.Run()
		return out.String(), out.truncated, err
	}
	keys, truncated, err := run(64<<10, "config", "--null", "--name-only", "--list")
	if err != nil || truncated {
		return "", errors.New("无法确认工作区的 Git 配置")
	}
	for key := range strings.SplitSeq(keys, "\x00") {
		key = strings.ToLower(key)
		if key == "core.worktree" || strings.HasPrefix(key, "filter.") && (strings.HasSuffix(key, ".clean") || strings.HasSuffix(key, ".process")) {
			return "", errors.New("工作区设置了自定义工作树或可执行 Git 过滤器，请手动核对 Git 状态")
		}
	}
	status, truncated, err := run(12<<10, "status", "--porcelain=v1", "--untracked-files=normal", "--ignore-submodules=all", "--", ".")
	if err != nil {
		return "", errors.New("当前目录不是 Git 工作区，或无法读取 Git 状态")
	}
	if truncated {
		status = boundedPlannerText(status+"\n", len(status))
	}
	if status == "" {
		return "当前目录没有 Git 改动（未检查子模块）", nil
	}
	return status, nil
}

func plannerList(root, requested string) (string, error) {
	base, err := plannerPath(root, requested, false)
	if err != nil {
		return "", err
	}
	entries := []string{}
	err = filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && plannerSkipDirectory(entry.Name()) && path != base {
			return filepath.SkipDir
		}
		if path == base {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if entry.IsDir() {
			rel += "/"
		}
		entries = append(entries, rel)
		if len(entries) >= 200 {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return "", err
	}
	if len(entries) == 0 {
		return "目录为空。", nil
	}
	return strings.Join(entries, "\n"), nil
}

func plannerSkipDirectory(name string) bool {
	return name == ".git" || name == "node_modules" || name == "dist" || name == ".next" || name == "target"
}

func plannerRead(root, requested string) (string, error) {
	path, err := plannerPath(root, requested, false)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("只能读取普通文件")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if bytes.ContainsRune(data, '\x00') {
		return "", errors.New("二进制文件不支持文本读取")
	}
	return requested + "\n" + boundedPlannerText(string(data), 64<<10), nil
}

func plannerSearch(root, requested, expression string) (string, error) {
	if expression == "" || len(expression) > 500 {
		return "", errors.New("搜索表达式为空或超过 500 字节")
	}
	matcher, err := regexp.Compile(expression)
	if err != nil {
		return "", errors.New("搜索正则表达式无效")
	}
	base, err := plannerPath(root, requested, false)
	if err != nil {
		return "", err
	}
	matches := []string{}
	err = filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if plannerSkipDirectory(entry.Name()) && path != base {
				return filepath.SkipDir
			}
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || bytes.ContainsRune(data, '\x00') {
			return nil
		}
		for number, line := range strings.Split(string(data), "\n") {
			if matcher.MatchString(line) {
				rel, _ := filepath.Rel(root, path)
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, number+1, boundedPlannerText(strings.TrimSpace(line), 400)))
				if len(matches) >= 100 {
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return "", err
	}
	if len(matches) == 0 {
		return "没有匹配的文本。", nil
	}
	return strings.Join(matches, "\n"), nil
}

func boundedPlannerText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit] + "\n…（内容已截断）"
}

func plannerContext(root string) (string, error) {
	listing, err := plannerList(root, ".")
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	builder.WriteString("工作区文件（最多 200 项）：\n")
	builder.WriteString(listing)
	for _, name := range []string{"AGENTS.md", "README.md"} {
		content, readErr := plannerRead(root, name)
		if readErr == nil {
			builder.WriteString("\n\n" + boundedPlannerText(content, 16<<10))
			continue
		}
		// A repository-relative symlink (AGENTS.md -> CLAUDE.md) is refused by the
		// planner policy. That refusal must skip the document, not abort the whole
		// planning context: a missing convenience file is not a load failure.
		if errors.Is(readErr, fs.ErrNotExist) || strings.Contains(readErr.Error(), "路径不存在") ||
			strings.Contains(readErr.Error(), "符号链接") {
			continue
		}
		return "", readErr
	}
	return builder.String(), nil
}

func plannerSkills(root string) (string, error) {
	documents := []string{}
	for _, directory := range []string{".agents/skills", ".kimi-code/skills"} {
		base, err := plannerPath(root, directory, false)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(base)
		if err != nil {
			return "", err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			content, readErr := plannerRead(root, filepath.Join(directory, entry.Name(), "SKILL.md"))
			if readErr == nil {
				documents = append(documents, boundedPlannerText(content, 12<<10))
				if len(documents) >= 10 {
					return strings.Join(documents, "\n\n"), nil
				}
			}
		}
	}
	if len(documents) == 0 {
		return "当前工作区未发现项目级 Skill；这不代表已加载用户级 Skill。", nil
	}
	return strings.Join(documents, "\n\n"), nil
}
