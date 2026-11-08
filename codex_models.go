package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"
)

const codexCatalogTimeout = 5 * time.Second

// Codex's catalog supplies model IDs and effort names, never arbitrary CLI arguments.
var codexCatalogID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
var codexCatalogEffort = regexp.MustCompile(`^[a-z]+$`)

type codexCatalogKey struct{ binary, dir string }
type codexCatalogEntry struct {
	checked time.Time
	presets []ModelPreset
	message string
}

var codexCatalogCache = struct {
	sync.Mutex
	entries map[codexCatalogKey]codexCatalogEntry
}{entries: make(map[codexCatalogKey]codexCatalogEntry)}

// commandPresets adds discovered Codex choices without replacing existing preset keys.
func commandPresets(cfg Settings, command string, refresh bool) ([]ModelPreset, string, bool) {
	agent := toolOf(command)
	presets := presetsFor(cfg, agent)
	if agent != "codex" {
		return presets, "", false
	}
	args, err := splitCommand(command)
	if err != nil || len(args) < 2 || args[1] != "exec" {
		return presets, "", false
	}
	catalog, message := cachedCodexCatalog(args[0], cfg.Workdir, refresh)
	if len(catalog) == 0 {
		return presets, message, false
	}
	have := make(map[string]bool, len(presets))
	for _, preset := range presets {
		have[preset.Key] = true
	}
	for _, preset := range catalog {
		if !have[preset.Key] {
			presets = append(presets, preset)
		}
	}
	return presets, message, true
}

func cachedCodexCatalog(binary, cwd string, refresh bool) ([]ModelPreset, string) {
	if cwd == "" {
		cwd = "."
	}
	if absolute, err := filepath.Abs(cwd); err == nil {
		cwd = absolute
	}
	key := codexCatalogKey{binary, cwd}
	codexCatalogCache.Lock()
	defer codexCatalogCache.Unlock()
	entry := codexCatalogCache.entries[key]
	if !refresh && time.Since(entry.checked) < modelCacheTTL {
		return entry.presets, entry.message
	}
	fresh, err := readCodexCatalog(binary, cwd)
	entry.checked = time.Now()
	switch {
	case err == nil && len(fresh) > 0:
		entry.presets, entry.message = fresh, ""
	case len(entry.presets) > 0:
		entry.message = "Could not refresh Codex models. Showing the last list."
	default:
		entry.message = "Could not discover Codex models. Showing configured and built-in presets."
	}
	codexCatalogCache.entries[key] = entry
	return entry.presets, entry.message
}

func readCodexCatalog(binary, cwd string) ([]ModelPreset, error) {
	ctx, cancel := context.WithTimeout(context.Background(), codexCatalogTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://")
	cmd.Dir = cwd
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() {
		input.Close()
		output.Close()
	})
	defer stop()
	defer func() {
		input.Close()
		cancel()
		_ = cmd.Wait()
	}()
	return queryCodexCatalog(input, io.LimitReader(output, runMaxBytes))
}

// queryCodexCatalog only initializes the local protocol and asks for metadata.
func queryCodexCatalog(input io.Writer, output io.Reader) ([]ModelPreset, error) {
	enc := json.NewEncoder(input)
	dec := json.NewDecoder(output)
	reply := func(id int, result any) error {
		for {
			var message struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := dec.Decode(&message); err != nil {
				return err
			}
			if string(message.ID) != fmt.Sprint(id) {
				continue // notifications may arrive before the reply
			}
			if len(message.Error) > 0 {
				return fmt.Errorf("Codex rejected model discovery")
			}
			return json.Unmarshal(message.Result, result)
		}
	}
	if err := enc.Encode(D{"method": "initialize", "id": 1, "params": D{"clientInfo": D{"name": "dlg", "version": versionString()}}}); err != nil {
		return nil, err
	}
	var initialized json.RawMessage
	if err := reply(1, &initialized); err != nil {
		return nil, err
	}
	if err := enc.Encode(D{"method": "initialized"}); err != nil {
		return nil, err
	}

	var presets []ModelPreset
	seenKeys := map[string]bool{}
	seenCursors := map[string]bool{}
	cursor := ""
	for id := 2; ; id++ {
		params := D{"limit": 50, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := enc.Encode(D{"method": "model/list", "id": id, "params": params}); err != nil {
			return nil, err
		}
		var page struct {
			Data []struct {
				Model       string `json:"model"`
				DisplayName string `json:"displayName"`
				Hidden      bool   `json:"hidden"`
				Default     string `json:"defaultReasoningEffort"`
				Efforts     []struct {
					Name string `json:"reasoningEffort"`
				} `json:"supportedReasoningEfforts"`
			} `json:"data"`
			NextCursor string `json:"nextCursor"`
		}
		if err := reply(id, &page); err != nil {
			return nil, err
		}
		for _, model := range page.Data {
			if model.Hidden || !codexCatalogID.MatchString(model.Model) {
				continue
			}
			var efforts []string
			for _, effort := range model.Efforts {
				if codexCatalogEffort.MatchString(effort.Name) {
					efforts = append(efforts, effort.Name)
				}
			}
			// Prefer the CLI's default when first selecting a discovered model.
			if i := slices.Index(efforts, model.Default); i > 0 {
				efforts[0], efforts[i] = efforts[i], efforts[0]
			}
			if len(efforts) == 0 {
				efforts = []string{""}
			}
			for _, effort := range efforts {
				key := "codex:" + model.Model + ":" + effort
				if seenKeys[key] {
					continue
				}
				seenKeys[key] = true
				presets = append(presets, ModelPreset{Agent: "codex", Key: key,
					Label: orDefault(model.DisplayName, model.Model), Model: model.Model, Effort: effort})
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			return presets, nil
		}
		if seenCursors[cursor] {
			return nil, fmt.Errorf("Codex model catalog repeated a cursor")
		}
		seenCursors[cursor] = true
	}
}
