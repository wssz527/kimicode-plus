package scheduler

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func CheckGitWorktree(ctx context.Context, cwd string) GitWorktreeStatus {
	cleaned := strings.TrimSpace(cwd)
	if cleaned == "" || !filepath.IsAbs(cleaned) {
		return GitWorktreeStatus{Clean: true}
	}
	info, err := os.Stat(cleaned)
	if err != nil || !info.IsDir() {
		return GitWorktreeStatus{Clean: true, Error: "目录不存在或无法访问"}
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	revCmd := exec.CommandContext(checkCtx, "git", "-C", cleaned, "rev-parse", "--is-inside-work-tree")
	var revOut bytes.Buffer
	revCmd.Stdout = &revOut
	if err := revCmd.Run(); err != nil {
		return GitWorktreeStatus{IsGitRepo: false, Clean: true}
	}
	if strings.TrimSpace(revOut.String()) != "true" {
		return GitWorktreeStatus{IsGitRepo: false, Clean: true}
	}

	statusCmd := exec.CommandContext(checkCtx, "git", "-C", cleaned, "status", "--porcelain")
	var statusOut bytes.Buffer
	statusCmd.Stdout = &statusOut
	if err := statusCmd.Run(); err != nil {
		return GitWorktreeStatus{IsGitRepo: true, Clean: false, Error: "无法读取 Git 工作区状态"}
	}
	raw := strings.TrimRight(statusOut.String(), "\r\n")
	if raw == "" {
		return GitWorktreeStatus{IsGitRepo: true, Clean: true}
	}
	lines := strings.Split(raw, "\n")
	sample := make([]string, 0, 5)
	dirtyCount := 0
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		dirtyCount++
		if len(sample) < 5 {
			sample = append(sample, strings.TrimSpace(trimmed))
		}
	}
	return GitWorktreeStatus{
		IsGitRepo:  true,
		Clean:      dirtyCount == 0,
		DirtyCount: dirtyCount,
		Sample:     sample,
	}
}
