package scheduler

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitExecutables lists the places git is installed when PATH does not carry it.
// A launchd or GUI-launched core often has a minimal PATH, and treating "git not
// found" as "not a repository" would let an unattended yolo task run against a
// dirty worktree.
var gitExecutables = []string{"git", "/opt/homebrew/bin/git", "/usr/local/bin/git", "/usr/bin/git"}

// executablePath is the test seam for LookPath and the executable-bit check.
var executablePath = func(name string) (string, error) { return exec.LookPath(name) }

// resolveGitExecutable returns the first usable git binary. A bare command name
// must be resolved through PATH before it is accepted; without that, the first
// entry would always win and every absolute fallback below it would be dead
// code, so a GUI-launched core with a minimal PATH would never find git.
func resolveGitExecutable() (string, error) {
	for _, binary := range gitExecutables {
		if strings.ContainsRune(binary, filepath.Separator) {
			info, err := os.Stat(binary)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				continue
			}
			return binary, nil
		}
		resolved, err := executablePath(binary)
		if err != nil || resolved == "" {
			continue
		}
		return resolved, nil
	}
	return "", errors.New("git executable not found")
}

func gitCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	binary, err := resolveGitExecutable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	// Force the untranslated message so the "not a repository" check below does
	// not depend on the user's system language.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	return cmd, nil
}

// hasGitMarker walks up from dir looking for a .git directory or file. It decides
// whether a failure to run git must be treated as an unknown repository state.
func hasGitMarker(dir string) bool {
	current := filepath.Clean(dir)
	for {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

// CheckGitWorktree is fail-closed. When a .git marker exists but git cannot be
// run, times out, or reports an error other than "not a git repository", the
// worktree is reported as an unclean repository so preflight blocks an
// unattended task instead of assuming it is safe.
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

	repoPresent := hasGitMarker(cleaned)
	revCmd, cmdErr := gitCommand(checkCtx, "-C", cleaned, "rev-parse", "--is-inside-work-tree")
	if cmdErr != nil {
		if repoPresent {
			return GitWorktreeStatus{IsGitRepo: true, Clean: false, Error: "无法运行 git，无法确认工作区是否干净"}
		}
		return GitWorktreeStatus{IsGitRepo: false, Clean: true}
	}
	var revOut, revErr bytes.Buffer
	revCmd.Stdout = &revOut
	revCmd.Stderr = &revErr
	if err := revCmd.Run(); err != nil {
		if isNotARepository(revErr.String()) {
			return GitWorktreeStatus{IsGitRepo: false, Clean: true}
		}
		if repoPresent {
			return GitWorktreeStatus{IsGitRepo: true, Clean: false, Error: "无法读取 Git 工作区状态"}
		}
		return GitWorktreeStatus{IsGitRepo: false, Clean: true}
	}
	if strings.TrimSpace(revOut.String()) != "true" {
		return GitWorktreeStatus{IsGitRepo: false, Clean: true}
	}

	statusCmd, cmdErr := gitCommand(checkCtx, "-C", cleaned, "status", "--porcelain")
	if cmdErr != nil {
		return GitWorktreeStatus{IsGitRepo: true, Clean: false, Error: "无法运行 git，无法确认工作区是否干净"}
	}
	var statusOut, statusErr bytes.Buffer
	statusCmd.Stdout = &statusOut
	statusCmd.Stderr = &statusErr
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

func isNotARepository(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "not a git repository") || strings.Contains(lower, "不是 git 仓库")
}
