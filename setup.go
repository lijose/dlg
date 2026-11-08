package main

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Tools are capabilities, not role identities: several reviewers may use the same tool.
func toolOf(command string) string {
	a := strings.TrimSuffix(agentOf(command), ".exe")
	if a == "cursor-agent" || a == "agent" {
		return "cursor"
	}
	return a
}

func projectSettingKeys(l *Ledger) (map[string]bool, error) {
	rows, err := l.all("SELECT key FROM settings")
	keys := map[string]bool{}
	for _, row := range rows {
		keys[row.str("key")] = true
	}
	return keys, err
}

func globalSettings() (Settings, error) {
	s := defaultSettings()
	err := applyGlobalConfig(&s)
	return s, err
}

func setupInfo(l *Ledger) (D, error) {
	cfg, err := l.settings()
	if err != nil {
		return nil, err
	}
	keys, err := projectSettingKeys(l)
	if err != nil {
		return nil, err
	}
	raw, err := readGlobalConfig()
	if err != nil {
		return nil, err
	}
	global, err := globalSettings()
	if err != nil {
		return nil, err
	}
	agents := []D{}
	for _, a := range knownAgents {
		bin := a.name
		if bin == "cursor" {
			bin = "cursor-agent"
		}
		path, _ := exec.LookPath(bin)
		if path == "" && a.name == "cursor" {
			path, _ = exec.LookPath("agent")
		}
		choices := []modelView{}
		for _, m := range presetsFor(Settings{}, a.name) {
			choices = append(choices, modelView{m.Key, m.Label, m.contextLabel(), m.Model, m.Context, m.Effort})
		}
		primary, implement, _ := knownAgent(a.name)
		agents = append(agents, D{"models": choices, "name": a.name, "installed": path != "", "path": path, "primary": primary, "implement": implement})
	}
	source := "not configured"
	if _, ok := raw["primary_command"]; ok {
		source = "global"
	}
	if keys["primary_command"] {
		source = "project"
	}
	installed := false
	if words, e := splitCommand(cfg.PrimaryCommand); e == nil && len(words) > 0 {
		_, e = exec.LookPath(words[0])
		installed = e == nil
	}
	dir := resolvedWorkdir(cfg.Workdir)
	return D{"agents": agents, "primary_source": source, "configured": source != "not configured", "installed": installed,
		"tool": toolOf(cfg.PrimaryCommand), "project_keys": keys, "global_settings": global, "global_configured": raw["primary_command"] != nil,
		"plain_folder": dir != "" && !inGitWorktree(dir), "standard_codex_analysis": standardCodexAnalysis(cfg.PrimaryCommand),
		"codex_plain_folder_enabled": dir != "" && dir == cfg.CodexPlainFolder,
		"project":                    filepath.Base(filepath.Dir(filepath.Dir(l.path))), "global_path": mustGlobalPath()}, nil
}
func mustGlobalPath() string { p, _ := globalConfigPath(); return p }

func choiceViews(cfg Settings, command string, refresh bool) ([]modelView, string, string) {
	if listingAgent(command) {
		copy := cfg
		copy.SecondaryCommands = map[string]string{"_choices": command}
		list, msg := listRunnerModels(copy, "_choices", refresh)
		if list == nil {
			list = []modelView{}
		}
		return list, msg, "Installed CLI"
	}
	list, msg, discovered := commandPresets(cfg, command, refresh)
	views := []modelView{}
	for _, m := range list {
		views = append(views, modelView{m.Key, m.Label, m.contextLabel(), m.Model, m.Context, m.Effort})
	}
	if discovered {
		return views, msg, "Codex CLI catalog and configured presets"
	}
	return views, msg, "Built-in / terminal-configured presets; account access not checked"
}

func (s *server) setupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/setup", api(func(r *http.Request) (any, error) { return setupInfo(s.l) }))
	mux.HandleFunc("GET /api/agent-models", api(func(r *http.Request) (any, error) {
		cfg, err := s.l.settings()
		if err != nil {
			return nil, err
		}
		projectCfg := cfg
		if r.URL.Query().Get("scope") == "global" {
			cfg, err = globalSettings()
			if err != nil {
				return nil, err
			}
		}
		command := cfg.PrimaryCommand
		if reviewer := r.URL.Query().Get("reviewer"); reviewer != "" {
			command = cfg.SecondaryCommands[reviewer]
			if command == "" {
				return D{"models": []modelView{}, "source": "Copy & paste", "error": ""}, nil
			}
			if !slices.Contains(projectCfg.RunnerConfirmed, command) {
				return D{"models": []modelView{}, "source": "Awaiting terminal approval", "error": "Approve this runner in your project terminal before listing models."}, nil
			}
		} else if setup, err := setupInfo(s.l); err != nil {
			return nil, err
		} else if setup["configured"] != true && r.URL.Query().Get("scope") != "global" {
			return D{"models": []modelView{}, "source": "Choose a primary agent first"}, nil
		}
		list, msg, source := choiceViews(cfg, command, r.URL.Query().Get("refresh") == "1")
		return D{"models": list, "error": msg, "source": source, "fixed": func() string {
			m := runnerModel(command)
			if m == "default" {
				return ""
			}
			return m
		}()}, nil
	}))
	// Only model keys and display preferences cross HTTP; executable settings stay CLI-only.
	mux.HandleFunc("PUT /api/agent-preferences", api(func(r *http.Request) (any, error) {
		var b struct {
			Scope  string `json:"scope"`
			Values D      `json:"values"`
		}
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		if b.Scope != "project" && b.Scope != "global" {
			return nil, ledgerErr("scope must be project or global")
		}
		cfg, err := s.l.settings()
		if err != nil {
			return nil, err
		}
		if b.Scope == "global" {
			cfg, err = globalSettings()
			if err != nil {
				return nil, err
			}
		}
		for k, v := range b.Values {
			switch k {
			case "default_analyse_model", "default_implement_model":
				key, ok := v.(string)
				if !ok {
					return nil, ledgerErr("model must be a preset key")
				}
				if key != "" && key != noModel {
					if _, _, err := resolveModel(cfg, key, k == "default_implement_model"); err != nil {
						return nil, err
					}
				}
			case "secondary_names", "secondary_with_code":
				data, _ := json.Marshal(v)
				var names []string
				if err := json.Unmarshal(data, &names); err != nil {
					return nil, ledgerErr("invalid reviewer names")
				}
				for _, name := range names {
					if err := checkName(name); err != nil {
						return nil, ledgerErr("invalid reviewer name")
					}
				}
			case "reviewer_labels":
				data, _ := json.Marshal(v)
				var labels map[string]string
				if err := json.Unmarshal(data, &labels); err != nil {
					return nil, ledgerErr("invalid reviewer labels")
				}
				for name, label := range labels {
					if err := checkName(name); err != nil || len(label) > 256 {
						return nil, ledgerErr("invalid reviewer label")
					}
				}
			case "review_models":
				data, _ := json.Marshal(v)
				var choices map[string]string
				if err := json.Unmarshal(data, &choices); err != nil {
					return nil, ledgerErr("invalid reviewer models")
				}
				for name, key := range choices {
					if key == "" || key == noModel {
						continue
					}
					command := cfg.SecondaryCommands[name]
					// Global cursor defaults can only be selected after this command has been
					// approved locally. Never execute a new command just to populate a menu.
					local, _ := s.l.settings()
					if !slices.Contains(local.RunnerConfirmed, command) {
						return nil, ledgerErr("approve this runner in a project terminal first")
					}
					list, _, _ := choiceViews(cfg, command, false)
					if _, _, err := reviewerCommand(cfg, name, key, list); err != nil {
						return nil, err
					}
				}
			default:
				return nil, ledgerErr("%s cannot be changed here", k)
			}
		}
		if b.Scope == "global" {
			return okOf(writeGlobalConfig(b.Values))
		}
		return s.l.updateSettings(b.Values, "user")
	}))
	mux.HandleFunc("POST /api/settings/inherit", api(func(r *http.Request) (any, error) {
		var b struct {
			Group string `json:"group"`
		}
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		groups := map[string][]string{"primary": {"primary_name", "primary_command", "implement_command", "models", "default_analyse_model", "default_implement_model", "model_list_command"}, "reviewers": {"secondary_names", "secondary_commands", "secondary_with_code", "review_models", "reviewer_labels"}}
		keys, ok := groups[b.Group]
		if !ok {
			return nil, ledgerErr("unknown settings group")
		}
		// Removing an override can only inherit an already terminal-configured command.
		global, err := readGlobalConfig()
		if err != nil {
			return nil, err
		}
		if b.Group == "primary" && global["primary_command"] == nil {
			return nil, ledgerErr("configure a global primary in your terminal first")
		}
		return okOf(s.l.tx(func(x *tx) {
			for _, key := range keys {
				x.exec("DELETE FROM settings WHERE key = ?", key)
			}
			x.event("user", "inherit_settings", nil, D{"group": b.Group})
		}))
	}))
}
