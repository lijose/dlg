// Global (per-user) defaults for the primary, the secondaries and their runners.
// Layering: built-in defaults < global file < this project's ledger settings.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// globalConfigPath is $DLG_CONFIG, else ~/.config/dlg/config.json.
func globalConfigPath() (string, error) {
	if p := os.Getenv("DLG_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".config", "dlg", "config.json"), nil
}

// readGlobalConfig returns the raw keys of the global file; a missing file is empty.
func readGlobalConfig() (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	path, err := globalConfigPath()
	if err != nil {
		return out, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, ledgerErr("invalid global config %s: %v", path, err)
	}
	return out, nil
}

// applyGlobalConfig decodes the global file over s; only the keys it holds change.
func applyGlobalConfig(s *Settings) error {
	raw, err := readGlobalConfig()
	if err != nil || len(raw) == 0 {
		return err
	}
	for _, k := range []string{"workdir", "last_problem_id", "runner_confirmed", "codex_plain_folder"} { // project-local by nature
		delete(raw, k)
	}
	obj, _ := json.Marshal(raw)
	return json.Unmarshal(obj, s)
}

// writeGlobalConfig stores values (same keys as Settings) in the global file.
var globalConfigMu sync.Mutex

func writeGlobalConfig(values D) error {
	globalConfigMu.Lock()
	defer globalConfigMu.Unlock()
	raw, err := readGlobalConfig()
	if err != nil {
		return err
	}
	for k, v := range values {
		raw[k], _ = json.Marshal(v)
	}
	path, _ := globalConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(raw, "", "  ")
	f, err := os.CreateTemp(filepath.Dir(path), ".dlg-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// cmdGlobalConfig handles `dlg config --global ...`.
func cmdGlobalConfig(args []string) error {
	const use = "usage: dlg config --global show | use <agent> | set-primary <name> <command> [implement-command]\n" +
		"       dlg config --global set-secondaries <name>... | set-runner <agent> <command> | unset-runner <agent>"
	var s Settings
	yes := false
	if len(args) > 1 && args[1] == "--yes" {
		yes = true
		args = append([]string{args[0]}, args[2:]...)
	}
	if len(args) == 0 {
		return usageError(use)
	}
	if err := applyGlobalConfig(&s); err != nil {
		return err
	}
	switch {
	case args[0] == "show" && len(args) == 1:
		raw, err := readGlobalConfig()
		if err != nil {
			return err
		}
		return printJSON(raw)
	case args[0] == "use" && len(args) == 2:
		primary, implement, ok := knownAgent(args[1])
		if !ok {
			return usageError("unknown built-in agent; use set-primary for custom commands")
		}
		return saveGlobalCommands(D{"primary_name": args[1], "primary_command": primary, "implement_command": implement, "default_analyse_model": "", "default_implement_model": "", "models": []ModelPreset{}, "model_list_command": ""}, yes)
	case args[0] == "set-primary" && (len(args) == 3 || len(args) == 4):
		impl := args[2]
		if len(args) == 4 {
			impl = args[3]
		}
		if err := checkName(args[1]); err != nil {
			return err
		}
		for _, c := range []string{args[2], impl} {
			if err := checkCommand(c); err != nil {
				return err
			}
		}
		return saveGlobalCommands(D{"primary_name": args[1], "primary_command": args[2], "implement_command": impl, "default_analyse_model": "", "default_implement_model": "", "models": []ModelPreset{}, "model_list_command": ""}, yes)
	case args[0] == "set-secondaries" && len(args) >= 2:
		return writeGlobalConfig(D{"secondary_names": args[1:]})
	case args[0] == "set-runner" && len(args) >= 3:
		command := strings.Join(args[2:], " ")
		if err := checkName(args[1]); err != nil {
			return err
		}
		if err := checkCommand(command); err != nil {
			return err
		}
		runners := map[string]string{}
		for k, v := range s.SecondaryCommands {
			runners[k] = v
		}
		runners[args[1]] = command
		return saveGlobalCommands(D{"secondary_commands": runners, "secondary_names": appendUnique(s.SecondaryNames, args[1])}, yes)
	case args[0] == "unset-runner" && len(args) == 2:
		runners := map[string]string{}
		for k, v := range s.SecondaryCommands {
			if k != args[1] {
				runners[k] = v
			}
		}
		return saveGlobalCommands(D{"secondary_commands": runners}, yes)
	}
	return usageError(use)
}

func saveGlobalCommands(values D, yes bool) error {
	fields := [][2]string{}
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		value := values[key]
		if text, ok := value.(string); ok {
			fields = append(fields, [2]string{key, text})
		} else {
			data, _ := json.Marshal(value)
			fields = append(fields, [2]string{key, string(data)})
		}
	}

	if !confirmSetting("dlg will store global defaults (inherited by projects without overrides):", fields, yes) {
		return ledgerErr("nothing stored: approve global commands in a terminal")
	}
	return writeGlobalConfig(values)
}

func appendUnique(names []string, name string) []string {
	for _, n := range names {
		if n == name {
			return names
		}
	}
	return append(names, name)
}
