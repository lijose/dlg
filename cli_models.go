// Model selection from the command line: list presets, analyse with one, set defaults,
// seed presets, and parse add-model's structured flags.
package main

import (
	"fmt"
	"os"
	"strings"
)

func cmdModels(args []string) error {
	fs := newFlags("models")
	refresh := fs.Bool("refresh", false, "re-run the model list command")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usageError("usage: dlg models [--refresh] [--json]")
	}
	return withLedger(func(l *Ledger, cfg Settings) error {
		list, errMsg := availableModels(cfg, *refresh)
		if errMsg != "" {
			warn([]string{errMsg})
		}
		warn(versionDrift(cfg, list))
		if *asJSON {
			views := []modelView{}
			for _, m := range list {
				views = append(views, modelView{m.Key, m.Label, m.contextLabel(), m.Model, m.Context, m.Effort})
			}
			return printJSON(D{"models": views, "error": errMsg})
		}
		for _, m := range list {
			mark := ""
			if m.Key == cfg.DefaultAnalyseModel {
				mark += " [analyse default]"
			}
			if m.Key == cfg.DefaultImplementModel {
				mark += " [implement default]"
			}
			fmt.Printf("%s\t%s\t%s\t%s\t%s%s\n", m.Key, m.Label, m.Model, m.contextLabel(), m.Effort, mark)
		}
		return nil
	})
}

// versionDrift warns when the installed CLI differs from the version stored on a preset.
func versionDrift(cfg Settings, list []ModelPreset) []string {
	installed, seen := "", false
	var out []string
	for _, m := range list {
		if m.CLIVersion == "" {
			continue
		}
		if !seen {
			installed, seen = cliVersion(agentOf(cfg.PrimaryCommand)), true
		}
		if installed != "" && installed != m.CLIVersion {
			out = append(out, fmt.Sprintf("preset %q was set up for CLI version %q, installed is %q; re-check its flags", m.Key, m.CLIVersion, installed))
		}
	}
	return out
}

// withLedger opens the ledger directly (as `dlg config` does) with its settings.
func withLedger(fn func(l *Ledger, cfg Settings) error) error {
	path, err := ledgerPath()
	if err != nil {
		return err
	}
	l, err := openLedger(path)
	if err != nil {
		return err
	}
	defer l.close()
	cfg, err := l.settings()
	if err != nil {
		return err
	}
	return fn(l, cfg)
}

func cmdAnalyse(args []string) error {
	fs := newFlags("analyse")
	model := fs.String("model", "", "preset key (see `dlg models`); none = base command")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("usage: dlg analyse <id> [--model KEY]")
	}
	pid, err := parseID(pos[0])
	if err != nil {
		return err
	}
	return withLedger(func(l *Ledger, cfg Settings) error {
		if _, err := l.require("problems", pid); err != nil {
			return err
		}
		if _, _, err := resolveModel(cfg, *model, false); err != nil {
			return err
		}
		res, err := (&server{l}).analyseRun(pid, runOpts{model: *model})
		if err != nil {
			return err
		}
		return printJSON(res)
	})
}

// addModelPreset parses `add-model <key> <label> <context-label> [flags|args...]`. --model,
// --context and --effort are structured fields; everything else, and everything after `--`,
// is raw Args.
func addModelPreset(args []string) (ModelPreset, error) {
	m := ModelPreset{Key: args[0], Label: args[1], ContextLabel: args[2]}
	var raw []string
	for i := 3; i < len(args); i++ {
		if args[i] == "--" {
			raw = append(raw, args[i+1:]...)
			break
		}
		dst := map[string]*string{"--model": &m.Model, "--context": &m.Context, "--effort": &m.Effort}[args[i]]
		switch {
		case dst == nil:
			raw = append(raw, args[i])
		case i+1 >= len(args):
			return m, usageError(args[i] + " needs a value")
		default:
			i++
			*dst = args[i]
		}
	}
	m.Args = strings.Join(raw, " ")
	if m.Context != "" {
		m.ContextLabel = m.Context
	}
	return m, nil
}

func cmdSetDefaultModel(l *Ledger, cfg Settings, args []string) error {
	if len(args) != 2 || (args[0] != "analyse" && args[0] != "implement") {
		return usageError("usage: dlg config set-default-model <analyse|implement> <key|none>")
	}
	key := args[1]
	if key == "none" {
		key = ""
	} else if _, _, err := resolveModel(cfg, key, args[0] == "implement"); err != nil {
		return err
	}
	_, err := l.updateSettings(D{"default_" + args[0] + "_model": key}, "user")
	return err
}

func cmdInitModels(l *Ledger, cfg Settings, args []string) error {
	agent := agentOf(cfg.PrimaryCommand)
	if len(args) == 1 {
		agent = args[0]
	} else if len(args) > 1 {
		return usageError("usage: dlg config init-models [claude|codex]")
	}
	if _, ok := agentTemplates[agent]; !ok {
		return usageError(fmt.Sprintf("no model templates for %q; use claude or codex", agent))
	}
	seeded, refused := seedPresets(agent, cliVersion(agent))
	for _, r := range refused {
		fmt.Fprintln(os.Stderr, "refused:", r)
	}
	if len(seeded) == 0 {
		return ledgerErr("no verified presets for %s; nothing seeded", agent)
	}
	models := cfg.Models
	for _, s := range seeded {
		models = withPreset(models, s)
	}
	_, err := l.updateSettings(D{"models": models}, "user")
	return err
}
