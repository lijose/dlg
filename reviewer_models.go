// Reviewer models: a reviewer's run can use a model picked in the UI. The choice comes only
// from the runner CLI's own list (cursor-agent --list-models) and is passed as one --model
// argument after the approved command; the command itself and its approval stay CLI-only.
package main

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// A model id is one argument: no spaces, quotes or leading '-' (it must not read as a flag).
var reviewerModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\[\]=,:-]*$`)

// cursorEfforts are the reasoning suffixes cursor-agent puts in its model ids
// (claude-opus-5-5-high, gpt-5.5-none); the picker shows them as a separate choice.
var cursorEfforts = []string{"none", "low", "medium", "high", "xhigh"}

// listingAgent reports whether the runner's CLI can list its models: cursor-agent only, whose
// --model and --list-models flags were checked against its --help.
func listingAgent(command string) bool {
	return slices.Contains([]string{"cursor-agent", "agent"}, agentOf(command))
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// parseCursorModels reads "id - Label" lines; the header and anything that is not a valid id are
// skipped. Colour codes are dropped: some environments make the CLI print them.
func parseCursorModels(out string) []modelView {
	list := []modelView{}
	for _, line := range strings.Split(ansiRe.ReplaceAllString(strings.ReplaceAll(out, "\r", ""), ""), "\n") {
		id, label, ok := strings.Cut(strings.TrimSpace(line), " - ")
		if !ok || !reviewerModelRe.MatchString(id) {
			continue
		}
		m := modelView{Key: id, Label: label, Model: id}
		for _, e := range cursorEfforts {
			if family, ok := strings.CutSuffix(id, "-"+e); ok {
				m.Model, m.Effort = family, e
			}
		}
		if strings.Contains(label, "1M") {
			m.Context, m.ContextLabel = "1m", "1M"
		}
		list = append(list, m)
	}
	return list
}

var runnerModelCache struct {
	mu sync.Mutex
	at map[string]time.Time
	by map[string][]modelView
}

// listRunnerModels returns the models the reviewer's runner can use (cached for modelCacheTTL;
// the last list is served when a refresh fails). Empty for runners that cannot list models.
func listRunnerModels(cfg Settings, agent string, refresh bool) ([]modelView, string) {
	command := cfg.SecondaryCommands[agent]
	if !listingAgent(command) {
		return nil, ""
	}
	args, _ := splitCommand(command)
	bin := args[0]
	c := &runnerModelCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at == nil {
		c.at, c.by = map[string]time.Time{}, map[string][]modelView{}
	}
	if !refresh && time.Since(c.at[bin]) < modelCacheTTL {
		return c.by[bin], ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelListTimout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--list-models")
	cmd.Dir = cfg.Workdir
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if list := parseCursorModels(string(out)); err == nil && len(list) > 0 {
		c.at[bin], c.by[bin] = time.Now(), list
		return list, ""
	}
	msg := "could not list " + agent + "'s models"
	if err != nil {
		msg += ": " + err.Error()
	} else {
		msg += fmt.Sprintf(": no model ids in %q", strings.TrimSpace(string(out[:min(len(out), 120)])))
	}
	if c.by[bin] != nil {
		msg += " (showing the last list)"
	}
	return c.by[bin], msg
}

// reviewerCommand is the runner command with the chosen model. model "" means the reviewer's
// default (review_models), noModel the runner's own default. The model must be in list; a saved
// default is also accepted when the list cannot be read, since it was picked from a list before.
func reviewerCommand(cfg Settings, agent, model string, list []modelView) (string, string, error) {
	command := cfg.SecondaryCommands[agent]
	saved := cfg.ReviewModels[agent]
	if model == "" {
		model = saved
	}
	if model == "" || model == noModel {
		return command, "", nil
	}
	if !listingAgent(command) {
		return "", "", ledgerErr("%s's runner cannot pick a model; set it in the command with dlg config set-runner", agent)
	}
	if fixed := runnerModel(command); fixed != "default" {
		return "", "", ledgerErr("%s's runner command already sets --model %s", agent, fixed)
	}
	known := slices.ContainsFunc(list, func(m modelView) bool { return m.Key == model })
	if !known && !(len(list) == 0 && model == saved && reviewerModelRe.MatchString(model)) {
		return "", "", ledgerErr("%q is not one of %s's models", model, agent)
	}
	return command + " --model " + model, model, nil
}
