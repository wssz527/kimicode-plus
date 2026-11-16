package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"enhancedkimi/internal/provider"
	"enhancedkimi/internal/security"
)

func plannerToolDefinitions(options PlannerOptions) []map[string]any {
	if !options.Tools {
		return nil
	}
	definitions := []map[string]any{
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
	if call.Function.Name != "write_file" {
		result, err := plannerReadOnlyTool(root, call.Function.Name, input.Path, input.Pattern)
		if err != nil {
			return "工具未执行：" + err.Error(), nil
		}
		return result, ctx.Err()
	}
	if options.ReadOnly || !options.Tools {
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
	path, err := plannerPath(root, input.Path, true)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0755)
	}
	if err == nil {
		path, err = plannerPath(root, input.Path, true)
	}
	if err == nil {
		err = os.WriteFile(path, []byte(input.Content), 0644)
	}
	if err != nil {
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
	root, err := filepath.EvalSymlinks(root)
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
		candidate = filepath.Join(root, candidate)
	}
	candidate = filepath.Clean(candidate)
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("路径超出当前工作区")
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if !allowMissing || !errors.Is(err, fs.ErrNotExist) {
			return "", errors.New("路径不存在或无法访问")
		}
		resolved, err = filepath.EvalSymlinks(filepath.Dir(candidate))
		if err != nil {
			return "", errors.New("父目录不存在或无法访问")
		}
		resolved = filepath.Join(resolved, filepath.Base(candidate))
	}
	relative, err = filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("路径经符号链接超出当前工作区")
	}
	if info, statErr := os.Stat(resolved); statErr == nil && !info.Mode().IsRegular() && !info.IsDir() {
		return "", errors.New("只允许普通文件或目录")
	}
	return resolved, nil
}

func plannerReadOnlyTool(root, name, path, pattern string) (string, error) {
	switch name {
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
		} else if !errors.Is(readErr, fs.ErrNotExist) && !strings.Contains(readErr.Error(), "路径不存在") {
			return "", readErr
		}
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
