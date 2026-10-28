// `dlg run <id> <agent>`: opt-in runner for reviewers that have a CLI. The command comes
// from the secondary_commands setting, which only the command line can change. The handoff
// goes to its stdin; its stdout is stored raw as the reviewer's reply.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	runTimeout  = 10 * time.Minute
	runMaxBytes = 1 << 20 // larger replies are cut and flagged
)

// capWriter keeps the first max bytes and remembers that it dropped the rest. It never
// fails, so a chatty command is not killed by a broken pipe.
type capWriter struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	room := max(w.max-w.buf.Len(), 0)
	w.buf.Write(p[:min(room, len(p))])
	if len(p) > room {
		w.truncated = true
	}
	return len(p), nil
}

// runnerModel is the --model a reviewer's runner command passes, or "default".
func runnerModel(command string) string {
	args, _ := splitCommand(command)
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "--model="); ok {
			return v
		}
		if a == "--model" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return "default"
}

type runResult struct {
	stdout    string
	exitCode  int
	duration  time.Duration
	truncated bool
	usage     *Usage // from a stream-json result event, else nil
}

// execRunner runs command (no shell) with input on stdin. Failure, timeout and empty output are errors.
// Output is also copied to progress as it arrives (the UI job panel; io.Discard on the CLI).
func execRunner(parent context.Context, command, input, cwd string, timeout time.Duration, progress io.Writer) (*runResult, error) {
	args, err := splitCommand(command)
	if err != nil {
		return nil, AgentError(err.Error())
	}
	if len(args) == 0 {
		return nil, AgentError("no runner command configured")
	}
	args = withStreaming(args)
	fmt.Fprintf(progress, "$ %s\n", strings.Join(args, " "))
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = cwd
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = strings.NewReader(input)
	stdout, stderr := &capWriter{max: runMaxBytes}, &capWriter{max: 4096}
	cmd.Stdout, cmd.Stderr = io.MultiWriter(stdout, progress), io.MultiWriter(stderr, progress)
	var stream *streamReader
	if isStreamJSON(args) { // keep the events out of the reply; show them as readable progress
		stream = &streamReader{progress: progress}
		cmd.Stdout = stream
	}
	start := time.Now()
	err = cmd.Run()
	if stream != nil {
		stream.flush()
	}
	res := &runResult{duration: time.Since(start), truncated: stdout.truncated}
	switch {
	case parent.Err() != nil:
		return nil, AgentError("runner stopped")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, AgentError(fmt.Sprintf("runner timed out after %s", timeout))
	case errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist):
		return nil, AgentError("command not found: " + args[0])
	case err != nil:
		msg := strings.TrimSpace(stderr.buf.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, AgentError(msg)
	}
	res.exitCode = cmd.ProcessState.ExitCode()
	res.stdout = strings.ToValidUTF8(stdout.buf.String(), "")
	if stream != nil {
		if stream.result == nil || stream.failed { // a failed result still cost something: keep its usage
			return &runResult{usage: stream.usage}, AgentError("the runner ended without a result event; nothing was stored")
		}
		res.usage = stream.usage
		r := *stream.result
		res.truncated = len(r) > runMaxBytes
		res.stdout = strings.ToValidUTF8(r[:min(len(r), runMaxBytes)], "")
	}
	if strings.TrimSpace(res.stdout) == "" {
		return nil, AgentError("the runner produced no output; nothing was stored")
	}
	return res, nil
}

// confirmRunner asks once per command text before the ledger executes it for the first time.
func confirmRunner(l *Ledger, cfg Settings, command string, yes bool) error {
	if slices.Contains(cfg.RunnerConfirmed, command) {
		return nil
	}
	if !yes {
		if !isTerminal(os.Stdin) {
			return fmt.Errorf("%q has not been approved yet; run `dlg config set-runner <agent> <command>` or `dlg run` in a terminal once, or pass --yes", command)
		}
		fmt.Fprintf(os.Stderr, "dlg will run this command and pipe the handoff to its stdin:\n  %s\n(in %s)\nAllow? [y/N] ", command, cfg.Workdir)
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return errors.New("not confirmed")
		}
	}
	_, err := l.updateSettings(D{"runner_confirmed": append(slices.Clone(cfg.RunnerConfirmed), command)}, "user")
	return err
}

func cmdRun(args []string) error {
	fs := newFlags("run")
	yes := fs.Bool("yes", false, "approve the configured command without asking")
	timeout := fs.Duration("timeout", runTimeout, "give up after this long")
	model := fs.String("model", "", "the reviewer's model for this run (from its CLI's model list); default: review_models")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 && len(pos) != 2 {
		return usageError("usage: dlg run [id] <agent> [--yes] [--timeout D] [--model ID]")
	}
	agent := pos[len(pos)-1]
	path, err := ledgerPath()
	if err != nil {
		return err
	}
	l, err := openLedger(path) // settings and the audit trail are always read and written directly
	if err != nil {
		return err
	}
	defer l.close()
	var core Core = localCore{l}
	if rc := probeRemote(path); rc != nil {
		core = rc
	}
	cfg, err := l.settings()
	if err != nil {
		return err
	}
	var pid int64
	if len(pos) == 2 {
		if pid, err = parseID(pos[0]); err != nil {
			return err
		}
	} else if cfg.LastProblemID == nil {
		return usageError("usage: dlg run <id> <agent> (no problem was opened in the UI yet)")
	} else {
		pid = *cfg.LastProblemID // the problem last opened in the UI
	}
	command := cfg.SecondaryCommands[agent]
	if command == "" {
		return fmt.Errorf("no runner for %q. Chat-only agents use copy-paste (`dlg handoff %d %s`); "+
			"to run a CLI instead, set and approve it in a terminal: dlg config set-runner %s [command]", agent, pid, agent, agent)
	}
	if err := confirmRunner(l, cfg, command, *yes); err != nil {
		return err
	}
	out, meta, err := runSecondary(context.Background(), l, core, cfg, pid, agent, *model, *timeout, io.Discard)
	if err != nil {
		return err
	}
	warn(out.Warnings)
	if *asJSON {
		return printJSON(D{"review_id": out.ID, "warnings": out.Warnings, "meta": meta})
	}
	fmt.Printf("review %d stored (%d bytes%s). Next: dlg parse %d\n", out.ID, meta["bytes"],
		map[bool]string{true: ", truncated"}[meta["truncated"] == true], out.ID)
	return nil
}

// runSecondary pipes the handoff for agent to its runner and stores the reply as a review.
// Used by `dlg run` and the UI; the caller has made sure the command is approved.
// model is a reviewer model id, "" for the reviewer's default, or noModel (see reviewerCommand).
func runSecondary(ctx context.Context, l *Ledger, core Core, cfg Settings, pid int64, agent, model string, timeout time.Duration, progress io.Writer) (*ReviewOut, D, error) {
	if cfg.SecondaryCommands[agent] == "" {
		return nil, nil, ledgerErr("no runner for %q", agent)
	}
	list, _ := listRunnerModels(cfg, agent, false)
	command, _, err := reviewerCommand(cfg, agent, model, list)
	if err != nil {
		return nil, nil, err
	}
	h, err := core.Handoff(pid, agent, false)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(h.Text) == "" {
		return nil, nil, ledgerErr("the handoff for %s is empty; analyse the problem first", agent)
	}
	if err := l.record("user", "run_secondary", pid, D{"agent": agent, "command": command}); err != nil {
		return nil, nil, err
	}
	res, err := execRunner(ctx, command, h.Text, cfg.Workdir, timeout, progress)
	if res != nil {
		addJobUsage(ctx, res.usage, err != nil)
	}
	if res != nil && res.usage != nil {
		if recErr := l.record(agent, "usage", pid, usageMeta(res.usage, "review", runnerModel(command), err != nil)); recErr != nil {
			return nil, nil, recErr
		}
	}
	if err != nil {
		if recErr := l.record(agent, "run_secondary_failed", pid, D{"error": err.Error()}); recErr != nil {
			return nil, nil, recErr
		}
		return nil, nil, err
	}
	if _, err := core.Handoff(pid, agent, true); err != nil { // the text was sent: the next one is a delta
		return nil, nil, err
	}
	id := make([]byte, 8)
	rand.Read(id)
	meta := D{"run_id": hex.EncodeToString(id), "command": command, "exit_code": res.exitCode,
		"duration_ms": res.duration.Milliseconds(), "truncated": res.truncated}
	if res.usage != nil {
		meta["usage"] = res.usage
	}
	out, err := core.Review(pid, agent, res.stdout, meta)
	if err != nil {
		// The handoff is already marked sent, so keep the reply where the user can store it again.
		file := filepath.Join(filepath.Dir(l.path), fmt.Sprintf("unsaved-reply-%d-%s-%s.md", pid, agent, meta["run_id"]))
		if werr := os.WriteFile(file, []byte(res.stdout), 0o600); werr != nil {
			return nil, nil, fmt.Errorf("%w (and the reply could not be saved: %v)", err, werr)
		}
		return nil, nil, fmt.Errorf("%w; the reply was saved to %s, store it with: dlg review %d %s --file %s", err, file, pid, agent, file)
	}
	meta["bytes"] = len(res.stdout) // for the CLI message only; not stored with the review
	return out, meta, nil
}
