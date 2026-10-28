// Model presets: named extra arguments for the primary/implementation command. Presets are
// CLI-only config (`dlg config add-model`); the web sends only a preset key, never flags.
package main

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	modelCacheTTL   = 5 * time.Minute
	modelListTimout = 15 * time.Second
	noModel         = "none" // per-run choice: the base command without preset arguments
)

type ModelPreset struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	ContextLabel string `json:"context_label"` // display only
	Args         string `json:"args"`          // appended to the command, parsed like the command itself
	// Structured fields, rendered into arguments by the per-agent templates (model_templates.go).
	// A preset with only Args (legacy) skips rendering. ContextLabel is derived from Context.
	Model      string `json:"model,omitempty"`
	Context    string `json:"context,omitempty"`
	Effort     string `json:"effort,omitempty"`
	CLIVersion string `json:"cli_version,omitempty"` // detected by init-models
}

// modelView is what the web sees: no arguments.
type modelView struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	ContextLabel string `json:"context_label"`
	Model        string `json:"model,omitempty"`
	Context      string `json:"context,omitempty"`
	Effort       string `json:"effort,omitempty"`
}

var modelCache struct {
	mu   sync.Mutex
	at   time.Time
	cmd  string
	list []ModelPreset
}

// parseModelList reads lines of key<TAB>label<TAB>context<TAB>args.
func parseModelList(out string) []ModelPreset {
	var list []ModelPreset
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if strings.TrimSpace(f[0]) == "" {
			continue
		}
		m := ModelPreset{Key: strings.TrimSpace(f[0])}
		m.Label = m.Key
		for i, dst := range []*string{&m.Label, &m.ContextLabel, &m.Args} {
			if len(f) > i+1 && strings.TrimSpace(f[i+1]) != "" {
				*dst = strings.TrimSpace(f[i+1])
			}
		}
		list = append(list, m)
	}
	return list
}

// runModelList runs the configured list command (no shell, bounded by a timeout).
func runModelList(command, cwd string) ([]ModelPreset, error) {
	args, err := splitCommand(command)
	if err != nil || len(args) == 0 {
		return nil, AgentError("invalid model_list_command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelListTimout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = cwd
	cmd.WaitDelay = 2 * time.Second
	stdout := &capWriter{max: runMaxBytes}
	cmd.Stdout = stdout
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, AgentError("model list command timed out")
		}
		return nil, AgentError("model list command failed: " + err.Error())
	}
	return parseModelList(strings.ToValidUTF8(stdout.buf.String(), "")), nil
}

// availableModels merges the configured presets with the list command's output (cached for
// modelCacheTTL; stale entries are served when a refresh fails). Presets win on equal keys.
func availableModels(cfg Settings, refresh bool) (list []ModelPreset, errMsg string) {
	list = append(list, cfg.Models...)
	if cfg.ModelListCommand == "" {
		return list, ""
	}
	modelCache.mu.Lock()
	defer modelCache.mu.Unlock()
	if refresh || modelCache.cmd != cfg.ModelListCommand || time.Since(modelCache.at) > modelCacheTTL {
		fresh, err := runModelList(cfg.ModelListCommand, cfg.Workdir)
		switch {
		case err == nil:
			modelCache.at, modelCache.cmd, modelCache.list = time.Now(), cfg.ModelListCommand, fresh
		case modelCache.cmd == cfg.ModelListCommand:
			errMsg = err.Error() + " (showing the last list)"
		default:
			errMsg = err.Error()
		}
	}
	if modelCache.cmd == cfg.ModelListCommand {
		have := map[string]bool{}
		for _, m := range list {
			have[m.Key] = true
		}
		for _, m := range modelCache.list {
			if !have[m.Key] {
				list = append(list, m)
			}
		}
	}
	return list, errMsg
}

// resolveModel turns a preset key into the command to run. "" means the stage's default
// preset; noModel means the base command.
func resolveModel(cfg Settings, key string, implement bool) (command string, preset *ModelPreset, err error) {
	command = cfg.PrimaryCommand
	if implement {
		command = cfg.ImplementCommand
	}
	if key == "" {
		key = cfg.DefaultAnalyseModel
		if implement {
			key = cfg.DefaultImplementModel
		}
	}
	if key == "" || key == noModel {
		return command, nil, nil
	}
	list, _ := availableModels(cfg, false)
	for _, m := range list {
		if m.Key == key {
			args, err := renderArgs(agentOf(command), m)
			if err != nil {
				return "", nil, ledgerErr("model %q: %v", key, err)
			}
			if _, err := splitCommand(args); err != nil {
				return "", nil, ledgerErr("model %q has invalid arguments: %v", key, err)
			}
			return strings.TrimSpace(command + " " + args), &m, nil
		}
	}
	return "", nil, ledgerErr("unknown model %q; add it with `dlg config add-model`", key)
}
