// Model presets: named extra arguments for the primary/implementation command. Presets are
// CLI-only config (`dlg config add-model`); the web sends only a preset key, never flags.
package main

import (
	"context"
	"os/exec"
	"slices"
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
	Agent        string `json:"agent,omitempty"` // empty for legacy custom presets
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
	list, errMsg, _ = commandPresets(cfg, cfg.PrimaryCommand, refresh)
	if listingAgent(cfg.PrimaryCommand) {
		views, msg, _ := choiceViews(cfg, cfg.PrimaryCommand, refresh)
		for _, v := range views {
			list = append(list, ModelPreset{Key: v.Key, Label: v.Label, Args: "--model " + v.Key})
		}
		return list, msg
	}
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
			errMsg = strings.TrimSpace(errMsg + " " + err.Error() + " (showing the last list)")
		default:
			errMsg = strings.TrimSpace(errMsg + " " + err.Error())
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
	plainFolder := !implement && cfg.CodexPlainFolder != "" && standardCodexAnalysis(command) &&
		resolvedWorkdir(cfg.Workdir) == cfg.CodexPlainFolder
	if plainFolder {
		command = strings.TrimSpace(command) + " --skip-git-repo-check --sandbox read-only"
	}
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
	list := presetsFor(cfg, toolOf(cfg.PrimaryCommand))
	if !slices.ContainsFunc(list, func(preset ModelPreset) bool { return preset.Key == key }) {
		list, _ = availableModels(cfg, false)
	}
	for _, m := range list {
		if m.Key == key {
			args, err := renderArgs(toolOf(command), m)
			if err != nil {
				return "", nil, ledgerErr("model %q: %v", key, err)
			}
			words, err := splitCommand(args)
			if err != nil {
				return "", nil, ledgerErr("model %q has invalid arguments: %v", key, err)
			}
			if plainFolder {
				if err := checkPlainFolderPreset(words); err != nil {
					return "", nil, err
				}
			}
			return strings.TrimSpace(command + " " + args), &m, nil
		}
	}
	return "", nil, ledgerErr("unknown model %q; add it with `dlg config add-model`", key)
}

// Built-in choices require no setup command and never invoke a model. Persisted
// custom presets retain precedence; tool-specific presets cannot leak across tools.
func presetsFor(cfg Settings, agent string) []ModelPreset {
	out := []ModelPreset{}
	have := map[string]bool{}
	for _, m := range cfg.Models {
		if m.Agent == "" || m.Agent == agent {
			out = append(out, m)
			have[m.Key] = true
		}
	}
	for model, caps := range knownCaps[agent] {
		for _, context := range caps.Contexts {
			for _, effort := range caps.Efforts {
				m := ModelPreset{Agent: agent, Key: model + "-" + context + "-" + effort, Label: model, Model: model, Context: context, Effort: effort}
				if _, err := renderArgs(agent, m); err == nil && !have[m.Key] {
					out = append(out, m)
				}
			}
		}
	}
	slices.SortStableFunc(out, func(a, b ModelPreset) int { return strings.Compare(a.Key, b.Key) })
	return out
}
