package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCodexPlainFolderKeepsModelAndScope(t *testing.T) {
	l := configProject(t)
	dir, err := os.Getwd()
	must(t, err)
	must(t, writeGlobalConfig(D{
		"primary_name": "codex", "primary_command": "codex exec",
		"codex_plain_folder":    resolvedWorkdir(dir),
		"default_analyse_model": "chosen",
		"models":                []ModelPreset{{Key: "chosen", Model: "chosen-model", Args: "--color never"}},
	}))
	_, err = l.updateSettings(D{"workdir": dir}, "user")
	must(t, err)
	info, err := setupInfo(l)
	must(t, err)
	if info["plain_folder"] != true || info["codex_plain_folder_enabled"] != false {
		t.Fatalf("unexpected initial setup %v", info)
	}
	if err := cmdConfig([]string{"codex-folder", "plain"}); err == nil {
		t.Fatal("enabled plain-folder mode without terminal approval")
	}
	c := client{t, newServer(l)}
	if code, _ := c.call("PUT", "http://127.0.0.1/api/settings", `{"codex_plain_folder":"anything"}`, "X-Ledger", "1"); code != 400 {
		t.Fatalf("browser changed terminal-only setting, status %d", code)
	}
	must(t, cmdConfig([]string{"codex-folder", "--yes", "plain"}))
	cfg, err := l.settings()
	must(t, err)
	command, preset, err := resolveModel(cfg, "", false)
	must(t, err)
	if command != "codex exec --skip-git-repo-check --sandbox read-only -m chosen-model --color never" || preset == nil || preset.Key != "chosen" {
		t.Fatalf("unexpected analysis command or model: %s, %v", command, preset)
	}
	implementation, _, err := resolveModel(cfg, "", true)
	must(t, err)
	if implementation != cfg.ImplementCommand {
		t.Fatalf("plain-folder approval changed implementation: %s", implementation)
	}
	info, err = setupInfo(l)
	must(t, err)
	if info["codex_plain_folder_enabled"] != true || info["primary_source"] != "global" {
		t.Fatalf("approval not shown or global inheritance changed: %v", info)
	}
	conflicting := cfg
	conflicting.Models = []ModelPreset{{Key: "chosen", Args: "--cd=/another-folder"}}
	if _, _, err := resolveModel(conflicting, "", false); err == nil {
		t.Fatal("preset redirected an approved run outside its folder")
	}
	cfg.Workdir = t.TempDir()
	command, _, err = resolveModel(cfg, "", false)
	must(t, err)
	if strings.Contains(command, "--skip-git-repo-check") {
		t.Fatalf("approval followed a change to the workdir: %s", command)
	}
	must(t, cmdConfig([]string{"codex-folder", "--yes", "git"}))
	cfg, err = l.settings()
	must(t, err)
	if cfg.CodexPlainFolder != "" || cfg.DefaultAnalyseModel != "chosen" {
		t.Fatalf("disabling changed the model or kept the opt-in: %+v", cfg)
	}
	if _, err := exec.LookPath("git"); err == nil {
		must(t, exec.Command("git", "-C", dir, "init", "-q").Run())
		info, err = setupInfo(l)
		must(t, err)
		if info["plain_folder"] != false {
			t.Fatal("Git worktree was missed")
		}
	}
}
