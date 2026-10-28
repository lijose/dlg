// Per-agent templates: render a preset's structured Model, Context and Effort into command
// arguments. An entry that is not Verified (checked against the installed CLI) renders
// nothing and is refused, so no unverified flag can reach a run.
package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type tmpl struct {
	Verified bool
	Format   string // one %s: the model or effort value
}

type ctxTmpl struct {
	Verified    bool
	ModelSuffix string // appended to the model value (claude's [1m] alias)
	Args        string // extra arguments (codex -c model_context_window=N)
}

type agentTmpl struct {
	Model    tmpl
	Effort   tmpl
	Contexts map[string]ctxTmpl
}

// agentTemplates contains checked CLI syntax. TestTemplatesVerified gates releases.
// Verification covers argument compatibility, not availability on every user's account.
var agentTemplates = map[string]agentTmpl{
	"claude": { // verified against Claude Code 2.1.304: --model opus|sonnet[1m], --effort low
		Model:  tmpl{true, "--model %s"},
		Effort: tmpl{true, "--effort %s"},
		Contexts: map[string]ctxTmpl{
			"200k": {Verified: true},
			"1m":   {Verified: true, ModelSuffix: "[1m]"},
		},
	},
	"codex": { // Codex CLI 0.157.1: gpt-6-luna, low effort, 200k override smoke-tested.
		Model:  tmpl{true, "-m %s"},
		Effort: tmpl{true, "-c model_reasoning_effort=%s"},
		Contexts: map[string]ctxTmpl{
			"200k": {Verified: true, Args: "-c model_context_window=200000"},
		},
	},
}

type modelCaps struct{ Contexts, Efforts []string }

// knownCaps: model -> supported contexts and efforts per agent. Seeds `init-models` and
// drives the non-blocking add-model warning.
var knownCaps = map[string]map[string]modelCaps{
	"claude": {
		"sonnet": {[]string{"200k", "1m"}, []string{"low", "medium", "high"}},
		"opus":   {[]string{"200k", "1m"}, []string{"low", "medium", "high"}},
	},
	"codex": {
		"gpt-6-luna": {[]string{"200k"}, []string{"low"}},
	},
}

// withPreset returns models with m added, replacing any preset with the same key.
func withPreset(models []ModelPreset, m ModelPreset) []ModelPreset {
	out := slices.DeleteFunc(slices.Clone(models), func(o ModelPreset) bool { return o.Key == m.Key })
	return append(out, m)
}

// structured reports whether the preset uses the templates; legacy presets pass Args through.
func (m ModelPreset) structured() bool { return m.Model != "" || m.Context != "" || m.Effort != "" }

// contextLabel is derived from Context when set.
func (m ModelPreset) contextLabel() string {
	if m.Context != "" {
		return m.Context
	}
	return m.ContextLabel
}

// agentOf names the agent CLI of a command ("claude -p" -> "claude").
func agentOf(command string) string {
	args, err := splitCommand(command)
	if err != nil || len(args) == 0 {
		return ""
	}
	return filepath.Base(args[0])
}

// renderArgs returns the arguments for a preset: Args alone for a legacy preset, otherwise the
// rendered fields followed by Args. Unverified or unknown template entries are an error.
func renderArgs(agent string, m ModelPreset) (string, error) {
	if !m.structured() {
		return m.Args, nil
	}
	t, ok := agentTemplates[agent]
	if !ok {
		return "", ledgerErr("no model templates for agent %q (supported: claude, codex)", agent)
	}
	var parts []string
	model := m.Model
	if m.Context != "" {
		c, ok := t.Contexts[m.Context]
		if !ok {
			return "", ledgerErr("%s has no context %q", agent, m.Context)
		}
		if !c.Verified {
			return "", ledgerErr("%s context %q is not verified against the installed CLI", agent, m.Context)
		}
		if c.ModelSuffix != "" && model == "" {
			return "", ledgerErr("%s context %q needs a model", agent, m.Context)
		}
		model += c.ModelSuffix
		if c.Args != "" {
			parts = append(parts, c.Args)
		}
	}
	if m.Model != "" {
		if !t.Model.Verified {
			return "", ledgerErr("%s model flag is not verified against the installed CLI", agent)
		}
		parts = append([]string{fmt.Sprintf(t.Model.Format, model)}, parts...)
	}
	if m.Effort != "" {
		if !t.Effort.Verified {
			return "", ledgerErr("%s effort flag is not verified against the installed CLI", agent)
		}
		parts = append(parts, fmt.Sprintf(t.Effort.Format, m.Effort))
	}
	if m.Args != "" {
		parts = append(parts, m.Args)
	}
	return strings.Join(parts, " "), nil
}

// validatePreset refuses presets that cannot render and warns (non-blocking) about
// combinations outside knownCaps.
func validatePreset(agent string, m ModelPreset) (warnings []string, err error) {
	if _, err := renderArgs(agent, m); err != nil {
		return nil, err
	}
	if !m.structured() {
		return nil, nil
	}
	c, ok := knownCaps[agent][m.Model]
	if !ok {
		return []string{fmt.Sprintf("model %q is not in the known %s capabilities", m.Model, agent)}, nil
	}
	if m.Context != "" && !slices.Contains(c.Contexts, m.Context) {
		warnings = append(warnings, fmt.Sprintf("context %q is not known for %s %s (known: %s)", m.Context, agent, m.Model, strings.Join(c.Contexts, ", ")))
	}
	if m.Effort != "" && !slices.Contains(c.Efforts, m.Effort) {
		warnings = append(warnings, fmt.Sprintf("effort %q is not known for %s %s (known: %s)", m.Effort, agent, m.Model, strings.Join(c.Efforts, ", ")))
	}
	return warnings, nil
}

// cliVersion is the first line of `<agent> --version`, "" when it cannot be read.
var cliVersion = func(agent string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, agent, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

// seedPresets builds the small preset set for an agent (highest known effort per model and
// context). Combinations whose templates are unverified are refused and returned in refused.
func seedPresets(agent, version string) (seeded []ModelPreset, refused []string) {
	names := make([]string, 0, len(knownCaps[agent]))
	for name := range knownCaps[agent] {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		c := knownCaps[agent][name]
		effort := c.Efforts[len(c.Efforts)-1]
		for _, ctx := range c.Contexts {
			m := ModelPreset{Key: name + "-" + ctx + "-" + effort, Label: name, ContextLabel: ctx,
				Model: name, Context: ctx, Effort: effort, CLIVersion: version}
			if _, err := renderArgs(agent, m); err != nil {
				refused = append(refused, m.Key+": "+err.Error())
				continue
			}
			seeded = append(seeded, m)
		}
	}
	return seeded, refused
}
