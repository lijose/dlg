package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func fakeCodexCatalog() {
	if len(os.Args) < 2 || os.Args[1] != "app-server" {
		os.Exit(2)
	}
	dec := json.NewDecoder(os.Stdin)
	for {
		var message struct {
			Method string `json:"method"`
			Params D      `json:"params"`
			ID     int    `json:"id"`
		}
		if err := dec.Decode(&message); err != nil {
			return
		}
		switch message.Method {
		case "initialize":
			fmt.Printf("{\"id\":%d,\"result\":{}}\n", message.ID)
		case "initialized":
		case "model/list":
			if message.Params["cursor"] == "second-page" {
				fmt.Printf(`{"id":%d,"result":{"data":[{"id":"internal","model":"second-model","displayName":"Second"}],"nextCursor":null}}`+"\n", message.ID)
				return
			}
			fmt.Println(`{"method":"warning","params":{"message":"fixture notification"}}`)
			fmt.Printf(`{"id":%d,"result":{"data":[{"id":"internal","model":"test-model","displayName":"Test Model","defaultReasoningEffort":"medium","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"medium"}]},{"model":"hidden-model","hidden":true},{"model":"x --sandbox danger-full-access"}],"nextCursor":"second-page"}}`+"\n", message.ID)
		default:
			os.Exit(2)
		}
	}
}

func TestCodexCatalogServesPrimaryAndApprovedReviewer(t *testing.T) {
	l := configProject(t)
	dir, err := os.Getwd()
	must(t, err)
	bin := filepath.Join(t.TempDir(), "codex")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	executable, err := os.Executable()
	must(t, err)
	data, err := os.ReadFile(executable)
	must(t, err)
	must(t, os.WriteFile(bin, data, 0o755))
	t.Setenv("DLG_FAKE_AGENT", "codex-catalog")
	command := fmt.Sprintf("%q exec", bin)
	cfg, err := l.updateSettings(D{
		"primary_command": command, "workdir": dir,
		"secondary_commands": map[string]string{"reviewer-1": command},
		"runner_confirmed":   []string{command},
		"secondary_names":    []string{"reviewer-1"},
		"models":             []ModelPreset{{Agent: "codex", Key: "manual", Model: "manual-model", Label: "Manual"}},
	}, "user")
	must(t, err)
	c := client{t, newServer(l)}
	for _, path := range []string{"/api/models", "/api/agent-models?reviewer=reviewer-1"} {
		code, response := c.call("GET", "http://127.0.0.1"+path, "", "X-Ledger", "1")
		if code != 200 || response["error"] != "" {
			t.Fatalf("%s: %d %v", path, code, response)
		}
		models := response["models"].([]any)
		var keys []string
		for _, model := range models {
			keys = append(keys, model.(map[string]any)["key"].(string))
		}
		for _, key := range []string{"manual", "gpt-6-luna-200k-low", "codex:test-model:medium", "codex:second-model:"} {
			if !slices.Contains(keys, key) {
				t.Fatalf("%s missing %s: %v", path, key, keys)
			}
		}
		if len(keys) != 5 || slices.Index(keys, "codex:test-model:medium") > slices.Index(keys, "codex:test-model:low") {
			t.Fatalf("unexpected catalog order or entries: %v", keys)
		}
	}
	key := "codex:test-model:medium"
	primary, _, err := resolveModel(cfg, key, false)
	must(t, err)
	reviewer, _, err := reviewerCommand(cfg, "reviewer-1", key, nil)
	must(t, err)
	if !strings.HasSuffix(primary, " -m test-model -c model_reasoning_effort=medium") || primary != reviewer {
		t.Fatalf("catalog selection differed by role: %q, %q", primary, reviewer)
	}
	t.Setenv("DLG_FAKE_AGENT", "empty")
	list, message, _ := commandPresets(cfg, command, true)
	if !strings.Contains(message, "last list") || !slices.ContainsFunc(list, func(m ModelPreset) bool { return m.Key == key }) {
		t.Fatalf("refresh failure lost the last catalog: %v %q", list, message)
	}
	cfg.Workdir = t.TempDir()
	list, message, _ = commandPresets(cfg, command, false)
	if len(list) != 2 || !strings.Contains(message, "built-in presets") {
		t.Fatalf("cold failure did not fall back to presets: %v %q", list, message)
	}
}
