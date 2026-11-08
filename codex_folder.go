package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func resolvedWorkdir(dir string) string {
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return ""
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return ""
	}
	return resolved
}

func inGitWorktree(dir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func standardCodexAnalysis(command string) bool {
	words, err := splitCommand(command)
	return err == nil && len(words) == 2 && words[1] == "exec" &&
		strings.TrimSuffix(strings.ToLower(filepath.Base(words[0])), ".exe") == "codex"
}

// Model presets may add arguments, but cannot use this opt-in to run outside the
// approved folder or override the explicit read-only sandbox.
func checkPlainFolderPreset(args []string) error {
	for _, arg := range args {
		flag, _, _ := strings.Cut(arg, "=")
		switch flag {
		case "--cd", "--sandbox", "--full-auto", "--yolo", "--approve-for-me", "--not-so-yolo",
			"--dangerously-bypass-approvals-and-sandbox", "--worktree", "--skip-git-repo-check":
			return ledgerErr("model preset option %q conflicts with plain-folder analysis", flag)
		}
		if strings.HasPrefix(arg, "-C") || strings.HasPrefix(arg, "-s") {
			return ledgerErr("model preset option %q conflicts with plain-folder analysis", arg)
		}
	}
	return nil
}
