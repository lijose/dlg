// Global (per-user) defaults for the primary, the secondaries and their runners.
// Layering: built-in defaults < global file < this project's ledger settings.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	for _, k := range []string{"workdir", "last_problem_id", "runner_confirmed"} { // project-local by nature
		delete(raw, k)
	}
	obj, _ := json.Marshal(raw)
	return json.Unmarshal(obj, s)
}

// writeGlobalConfig stores values (same keys as Settings) in the global file.
func writeGlobalConfig(values D) error {
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
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// cmdGlobalConfig handles `dlg config --global ...`.
func cmdGlobalConfig(args []string) error {
	const use = "usage: dlg config --global show | set-primary <name> <command> [implement-command]\n" +
		"       dlg config --global set-secondaries <name>... | set-runner <agent> <command> | unset-runner <agent>"
	var s Settings
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
		return writeGlobalConfig(D{"primary_name": args[1], "primary_command": args[2], "implement_command": impl})
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
		return writeGlobalConfig(D{"secondary_commands": runners})
	case args[0] == "unset-runner" && len(args) == 2:
		runners := map[string]string{}
		for k, v := range s.SecondaryCommands {
			if k != args[1] {
				runners[k] = v
			}
		}
		return writeGlobalConfig(D{"secondary_commands": runners})
	}
	return usageError(use)
}
