package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const codexUsageEvents = `{"type":"thread.started","thread_id":"test-thread"}
{"type":"turn.started"}
{"type":"item.started","item":{"id":"tool-1","type":"command_execution","command":"ls"}}
{"type":"item.completed","item":{"id":"tool-1","type":"command_execution","command":"ls"}}
{"type":"item.completed","item":{"id":"message-1","type":"agent_message","text":"Final answer"}}
{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":60,"output_tokens":20,"reasoning_output_tokens":5}}
`

func fakeCodexUsage() {
	if len(os.Args) < 2 || os.Args[1] != "exec" || !slices.Contains(os.Args, "--json") {
		os.Exit(2)
	}
	_, _ = io.ReadAll(os.Stdin)
	fmt.Print(codexUsageEvents)
	switch os.Getenv("DLG_CODEX_OUTCOME") {
	case "fail":
		fmt.Println(`{"type":"turn.started"}`)
		fmt.Println(`{"type":"turn.failed","error":{"message":"fixture failure"}}`)
		os.Exit(1)
	case "wait":
		fmt.Println(`{"type":"turn.started"}`)
		time.Sleep(10 * time.Second)
	}
}

func TestCodexUsageParser(t *testing.T) {
	s := &streamReader{progress: io.Discard, codex: true}
	// Input chunks need not align with JSONL events.
	for _, b := range []byte(codexUsageEvents) {
		_, _ = s.Write([]byte{b})
	}
	u := s.finish(2*time.Second, false)
	if s.result == nil || *s.result != "Final answer" || u.InputTokens != 100 || u.CacheRead != 60 || u.OutputTokens != 20 || u.ReasoningTokens != 5 || u.ToolCalls != 1 || u.Turns != 1 || u.CostUSD != nil || u.Partial || u.DurationMS != 2000 || u.SessionID != "test-thread" {
		t.Fatalf("unexpected result: %+v, %v", u, s.result)
	}
	// A repeated completion must not duplicate the tokens. A new turn does add usage.
	s.event([]byte(`{"type":"turn.completed","usage":{"input_tokens":100}}`))
	s.event([]byte(`{"type":"turn.started"}`))
	_, _ = s.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":30,"output_tokens":4}}`))
	u = s.finish(3*time.Second, false)
	if u.InputTokens != 130 || u.OutputTokens != 24 || u.Turns != 2 {
		t.Fatalf("incorrect turn aggregation: %+v", u)
	}
	var total CostLine
	total.add(*u, 1, false)
	if total.Priced != 0 || total.ReasoningTokens != 5 || total.ToolCalls != 1 || total.Turns != 2 {
		t.Fatalf("incorrect ledger totals: %+v", total)
	}
	s.event([]byte(`{"type":"turn.started"}`))
	u = s.finish(4*time.Second, true)
	if !u.Partial || u.InputTokens != 130 {
		t.Fatalf("lost partial usage: %+v", u)
	}
}

func TestCodexUsageAdapters(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "codex")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	executable, err := os.Executable()
	must(t, err)
	data, err := os.ReadFile(executable)
	must(t, err)
	must(t, os.WriteFile(bin, data, 0755))
	t.Setenv("DLG_FAKE_AGENT", "codex-usage")
	command := fmt.Sprintf("%q exec", bin)
	for _, outcome := range []string{"success", "fail", "wait"} {
		t.Run(outcome, func(t *testing.T) {
			t.Setenv("DLG_CODEX_OUTCOME", outcome)
			ctx := context.Background()
			cancel := func() {}
			if outcome == "wait" {
				ctx, cancel = context.WithTimeout(ctx, time.Second)
			}
			defer cancel()
			out, u, err := runAgentCtx(ctx, command, "input", t.TempDir(), io.Discard)
			if u == nil || u.InputTokens != 100 || u.CostUSD != nil {
				t.Fatalf("primary lost usage: %+v, %v", u, err)
			}
			if outcome == "success" {
				must(t, err)
				if out != "Final answer" {
					t.Fatal(out)
				}
			} else if err == nil || !u.Partial {
				t.Fatalf("primary failure missing: %+v, %v", u, err)
			}
			timeout := 5 * time.Second
			if outcome == "wait" {
				timeout = time.Second
			}
			result, err := execRunner(context.Background(), command, "input", t.TempDir(), timeout, io.Discard)
			if result == nil || result.usage == nil || result.usage.InputTokens != 100 {
				t.Fatalf("reviewer lost usage: %+v, %v", result, err)
			}
			if outcome == "success" {
				must(t, err)
				if result.stdout != "Final answer" {
					t.Fatal(result.stdout)
				}
			} else if err == nil || !result.usage.Partial {
				t.Fatalf("reviewer failure missing: %+v, %v", result, err)
			}
		})
	}
}

func TestCodexStreamingAndImplementation(t *testing.T) {
	for _, args := range [][]string{{"codex", "exec"}, {"codex.exe", "exec", "--json"}, {"codex", "e", "--", "prompt"}} {
		got := withStreaming(args)
		if !isStreamJSON(got) || slices.Index(got, "--json") != slices.Index(withStreaming(got), "--json") || len(withStreaming(got)) != len(got) {
			t.Fatal(got)
		}
	}
	literal := []string{"codex", "exec", "--", "--json"}
	if isStreamJSON(literal) || !codexJSON(withStreaming(literal)) || len(withStreaming(literal)) != len(literal)+1 {
		t.Fatal("prompt text was mistaken for a JSON option")
	}
	_, command, ok := knownAgent("codex")
	if !ok || command != "codex exec --sandbox workspace-write" || strings.Contains(defaultSettings().ImplementCommand, "--full-auto") {
		t.Fatal(command)
	}
}

func TestUnknownTokensRemainUnknown(t *testing.T) {
	u := parseUsage([]byte(`{"type":"result","duration_ms":42}`))
	var total CostLine
	total.add(*u, 1, false)
	if total.TokenRuns != 0 || total.Priced != 0 {
		t.Fatalf("missing usage reported as measured: %+v", total)
	}
	u = parseUsage([]byte(`{"type":"result","usage":{"input_tokens":0,"output_tokens":0}}`))
	total.add(*u, 1, false)
	if total.TokenRuns != 1 {
		t.Fatal("an explicitly reported zero was lost")
	}
}
