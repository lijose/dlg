// Readable progress for agents that print stream-json events (cursor-agent, claude -p):
// thinking, tool calls and answer text go to the job panel as they arrive; only the final
// "result" text is kept as the output.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// withStreaming adds stream-json flags to known agents in print mode that do not choose an
// output format themselves, so the UI shows progress without the user editing commands.
func withStreaming(args []string) []string {
	if len(args) == 0 || !slices.ContainsFunc(args, func(a string) bool { return a == "-p" || a == "--print" }) ||
		slices.ContainsFunc(args, func(a string) bool { return a == "--output-format" || strings.HasPrefix(a, "--output-format=") }) {
		return args
	}
	switch filepath.Base(args[0]) {
	case "claude":
		return append(slices.Clone(args), "--output-format", "stream-json", "--verbose", "--include-partial-messages")
	case "cursor-agent", "agent":
		return append(slices.Clone(args), "--output-format", "stream-json", "--stream-partial-output")
	}
	return args
}

func isStreamJSON(args []string) bool {
	i := slices.Index(args, "--output-format")
	return slices.Contains(args, "--output-format=stream-json") || i >= 0 && i+1 < len(args) && args[i+1] == "stream-json"
}

type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	Message struct {
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			Name  string         `json:"name"`  // tool_use (claude)
			Input map[string]any `json:"input"` // tool_use (claude)
		} `json:"content"`
	} `json:"message"`
	Event struct { // claude --include-partial-messages
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	ToolCall map[string]json.RawMessage `json:"tool_call"` // cursor-agent
}

// Usage is what an agent's result event says about the run. Claude Code reports cost, tokens,
// turns and models; cursor-agent reports less. Missing fields stay empty: nothing is estimated.
type Usage struct {
	CostUSD      *float64 `json:"cost_usd,omitempty"` // nil: the agent reports no cost
	InputTokens  int64    `json:"input_tokens,omitempty"`
	OutputTokens int64    `json:"output_tokens,omitempty"`
	CacheRead    int64    `json:"cache_read_tokens,omitempty"`
	CacheWrite   int64    `json:"cache_write_tokens,omitempty"`
	DurationMS   int64    `json:"duration_ms,omitempty"`
	APIMS        int64    `json:"api_ms,omitempty"`
	Turns        int64    `json:"turns,omitempty"`
	Models       []string `json:"models,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
}

// parseUsage reads a result event; agents differ in naming (snake_case vs camelCase).
func parseUsage(b []byte) *Usage {
	var e map[string]any
	if json.Unmarshal(b, &e) != nil {
		return nil
	}
	tokens, _ := e["usage"].(map[string]any)
	num := func(m map[string]any, keys ...string) (float64, bool) {
		for _, k := range keys {
			if v, ok := m[k].(float64); ok {
				return v, true
			}
		}
		return 0, false
	}
	i := func(m map[string]any, keys ...string) int64 { v, _ := num(m, keys...); return int64(v) }
	u := &Usage{
		InputTokens:  i(tokens, "input_tokens", "inputTokens"),
		OutputTokens: i(tokens, "output_tokens", "outputTokens"),
		CacheRead:    i(tokens, "cache_read_input_tokens", "cacheReadInputTokens", "cacheReadTokens"),
		CacheWrite:   i(tokens, "cache_creation_input_tokens", "cacheCreationInputTokens", "cacheWriteTokens"),
		DurationMS:   i(e, "duration_ms", "durationMs"),
		APIMS:        i(e, "duration_api_ms", "durationApiMs"),
		Turns:        i(e, "num_turns", "numTurns"),
	}
	if c, ok := num(e, "total_cost_usd", "cost_usd", "costUSD", "totalCostUsd"); ok {
		u.CostUSD = &c
	}
	if models, ok := e["modelUsage"].(map[string]any); ok {
		u.Models = slices.Sorted(maps.Keys(models))
	}
	for _, k := range []string{"session_id", "sessionId"} {
		if s, ok := e[k].(string); ok && u.SessionID == "" {
			u.SessionID = s
		}
	}
	return u
}

// meta is the usage as contribution metadata, or nil when there is none.
func (u *Usage) meta() D {
	if u == nil {
		return nil
	}
	return D{"usage": u}
}

// streamReader turns stream-json lines into progress text and remembers the result.
type streamReader struct {
	progress io.Writer
	line     []byte
	seg      string // text shown since the last tool or thinking; a full message repeating it is skipped
	thinking bool
	result   *string
	failed   bool
	usage    *Usage
}

func (s *streamReader) Write(p []byte) (int, error) {
	s.line = append(s.line, p...)
	for {
		i := bytes.IndexByte(s.line, '\n')
		if i < 0 {
			return len(p), nil
		}
		s.event(s.line[:i])
		s.line = s.line[i+1:]
	}
}

// flush handles a last event that arrived without a trailing newline.
func (s *streamReader) flush() {
	if len(bytes.TrimSpace(s.line)) > 0 {
		s.event(s.line)
	}
	s.line = nil
}

func (s *streamReader) event(b []byte) {
	var e streamEvent
	if json.Unmarshal(b, &e) != nil {
		s.progress.Write(append(b, '\n')) // not an event: show it as is
		return
	}
	switch {
	case e.Type == "thinking" || e.Type == "stream_event" && e.Event.Delta.Type == "thinking_delta":
		if !s.thinking {
			fmt.Fprint(s.progress, "\n💭 thinking…\n")
			s.thinking, s.seg = true, ""
		}
	case e.Type == "stream_event" && e.Event.Delta.Type == "text_delta":
		s.text(e.Event.Delta.Text)
	case e.Type == "assistant":
		text := ""
		for _, c := range e.Message.Content {
			text += c.Text
			if c.Type == "tool_use" {
				s.tool(argSummary(c.Name, c.Input))
			}
		}
		if text != s.seg { // a full message repeats the deltas already shown
			s.text(text)
		}
	case e.Type == "tool_call" && e.Subtype == "started":
		s.tool(toolSummary(e.ToolCall))
	case e.Type == "user": // tool results: the next text is a new message
		s.seg = ""
	case e.Type == "result":
		s.result, s.failed, s.usage = &e.Result, e.IsError, parseUsage(b)
		fmt.Fprint(s.progress, "\n")
	}
}

func (s *streamReader) text(t string) {
	if t != "" {
		s.seg += t
		s.thinking = false
		fmt.Fprint(s.progress, t)
	}
}

func (s *streamReader) tool(summary string) {
	fmt.Fprintf(s.progress, "\n🔧 %s\n", summary)
	s.thinking, s.seg = false, ""
}

// argSummary is the tool name and its main argument, e.g. "Read: app.go".
func argSummary(name string, args map[string]any) string {
	for _, k := range []string{"command", "file_path", "path", "pattern", "query", "url"} {
		if a, ok := args[k].(string); ok && a != "" {
			return name + ": " + a
		}
	}
	return name
}

// toolSummary reads cursor-agent's {"shellToolCall": {"args": {...}}} shape.
func toolSummary(call map[string]json.RawMessage) string {
	for name, raw := range call {
		if !strings.HasSuffix(name, "ToolCall") {
			continue
		}
		var v struct {
			Args map[string]any `json:"args"`
		}
		json.Unmarshal(raw, &v)
		return argSummary(strings.TrimSuffix(name, "ToolCall"), v.Args)
	}
	return "tool"
}
