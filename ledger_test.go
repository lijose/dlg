package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as the fake agent CLI: the ledger runs it with the prompt on stdin.
func TestMain(m *testing.M) {
	switch os.Getenv("DLG_FAKE_AGENT") {
	case "1":
		b, _ := io.ReadAll(os.Stdin)
		fakeAgent(string(b))
		return
	case "stdin": // a runner: reads the handoff on stdin, answers in the reply format
		b, _ := io.ReadAll(os.Stdin)
		fmt.Printf("SUMMARY: read %d bytes\n[P1] AGREE: fine\n", len(b))
		return
	case "empty":
		return
	}
	os.Exit(m.Run())
}

func fakeAgent(prompt string) {
	var out any
	switch {
	case strings.Contains(prompt, "agreement board"):
		point := D{"key": "P1", "topic": "cache or index", "primary": "index", "status": "open"}
		changes := []string{}
		if strings.Contains(prompt, "Reply from gemini") {
			point["positions"], point["status"] = D{"gemini": "cache"}, "disputed"
			changes = []string{"recorded gemini's view on P1"}
		}
		out = D{"summary": "query scans the table", "changes": changes,
			"questions":    []D{{"question": "how big is the table?", "assumption": "10M rows"}},
			"subproblems":  []D{{"title": "sub"}},
			"solution":     D{"title": "Index", "description": "add (a,b)", "evidence": []D{{"quote": "x = 1", "source": "a.py:1"}}},
			"alternatives": []D{{"title": "Cache"}}, "open_points": []string{"migration window"},
			"points": []D{point}}
	case strings.Contains(prompt, "final solution proposal"):
		out = D{"title": "Index (consensus)", "description": "add (a,b) online", "rationale": "both agree"}
	default:
		fmt.Println("implemented")
		return
	}
	b, _ := json.Marshal(out)
	fmt.Printf("thinking...\n```json\n%s\n```\n", b)
}

func newLedger(t *testing.T) *Ledger {
	t.Helper()
	// A space in the path checks the sqlite URI escaping.
	l, err := openLedger(filepath.Join(t.TempDir(), "my ledger.db"))
	must(t, err)
	t.Cleanup(func() { l.close() })
	return l
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantLedgerError(t *testing.T, err error) {
	t.Helper()
	var le LedgerError
	if !errors.As(err, &le) {
		t.Fatalf("want LedgerError, got %v", err)
	}
}

func status(t *testing.T, l *Ledger, pid int64) string {
	t.Helper()
	s, _, _, err := l.problemState(pid)
	must(t, err)
	return s
}

func TestReselectingMakesApprovalStale(t *testing.T) {
	l := newLedger(t)
	pid, err := l.createProblem("p", "", nil, "user")
	must(t, err)
	a, err := l.addAlternative(pid, "A", "", "user")
	must(t, err)
	b, err := l.addAlternative(pid, "B", "", "user")
	must(t, err)
	_, err = l.selectAlternative(pid, &a, "fast")
	must(t, err)
	_, err = l.approve(pid, "")
	must(t, err)
	if s := status(t, l, pid); s != "approved" {
		t.Fatal(s)
	}
	_, err = l.selectAlternative(pid, &b, "changed my mind")
	must(t, err)
	if s := status(t, l, pid); s != "selected" {
		t.Fatal(s)
	}
	_, err = l.implementationPayload(pid)
	wantLedgerError(t, err)
}

func TestDeferredProblemCannotBeApproved(t *testing.T) {
	l := newLedger(t)
	pid, err := l.createProblem("p", "", nil, "user")
	must(t, err)
	_, err = l.selectAlternative(pid, nil, "later")
	must(t, err)
	_, err = l.approve(pid, "")
	wantLedgerError(t, err)
}

func TestPayloadLeavesOutOpenDisagreementsAndListsRejected(t *testing.T) {
	l := newLedger(t)
	pid, _ := l.createProblem("p", "", nil, "user")
	a, _ := l.addAlternative(pid, "A", "", "user")
	l.addAlternative(pid, "B", "", "user")
	l.addDisagreement(DisagreementIn{ProblemID: pid, RaisedBy: "claude", Summary: "still open", AlternativeID: &a, RecordedBy: "user"})
	closed, err := l.addDisagreement(DisagreementIn{ProblemID: pid, RaisedBy: "claude", Summary: "settled", AlternativeID: &a, RecordedBy: "user"})
	must(t, err)
	must(t, l.resolveDisagreement(closed, "reject", "A is fine", "", ""))
	_, err = l.selectAlternative(pid, &a, "simplest")
	must(t, err)
	_, err = l.approve(pid, "")
	must(t, err)
	p, err := l.implementationPayload(pid)
	must(t, err)
	if len(p.Resolved) != 1 || p.Resolved[0].Summary != "settled" {
		t.Fatalf("resolved: %+v", p.Resolved)
	}
	if len(p.OutOfScope) != 1 || p.OutOfScope[0].Title != "B" {
		t.Fatalf("out of scope: %+v", p.OutOfScope)
	}
	wantLedgerError(t, l.resolveDisagreement(closed, "accept", "", "", ""))
}

func TestImportRejectsUnknownColumnsAndSkipsSettings(t *testing.T) {
	l := newLedger(t)
	pid, _ := l.createProblem("p", "", nil, "user")
	_, err := l.updateSettings(D{"primary_command": "safe"}, "user")
	must(t, err)
	data, err := l.export()
	must(t, err)
	raw, _ := json.Marshal(data)
	var tampered map[string]any
	json.Unmarshal(raw, &tampered)
	row := tampered["tables"].(map[string]any)["problems"].([]any)[0].(map[string]any)
	row["evil) VALUES (1); --"] = "x"
	raw, _ = json.Marshal(tampered)

	parsed, err := parseExport(raw)
	must(t, err)
	other := newLedger(t)
	must(t, other.importData(parsed))
	problems, err := other.listProblems()
	must(t, err)
	if problems[0].num("id") != pid {
		t.Fatalf("problems: %v", problems)
	}
	s, _ := other.settings()
	if s.PrimaryCommand == "safe" {
		t.Fatal("imported file changed the agent command")
	}
	wantLedgerError(t, other.importData(parsed)) // not empty any more
}

func TestSettingsRejectUnknownKeysAndBadTypes(t *testing.T) {
	l := newLedger(t)
	_, err := l.updateSettings(D{"nope": 1}, "user")
	wantLedgerError(t, err)
	_, err = l.updateSettings(D{"primary_command": 5}, "user")
	wantLedgerError(t, err)
}

func TestSplitCommand(t *testing.T) {
	got, err := splitCommand(`claude -p --x "a \"b\" c" 'd e' f\ g`)
	must(t, err)
	if want := []string{"claude", "-p", "--x", `a "b" c`, "d e", "f g"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if _, err := splitCommand(`a "b`); err == nil {
		t.Fatal("want unterminated quote error")
	}
}

func TestParseAgentPrefersLastFencedBlock(t *testing.T) {
	text := "```json\n{\"summary\":\"s\",\"solution\":{\"title\":\"old\"}}\n```\nnope\n```json\n{\"summary\":\"s\",\"solution\":{\"title\":\"new\"}}\n```"
	a, err := parseAgent[AnalysisOut](text)
	must(t, err)
	if a.Solution.Title != "new" {
		t.Fatal(a)
	}
	bad := `{"summary":"s","solution":{"title":"x"},"points":[{"key":"P1","topic":"t","status":"maybe"}]}`
	if _, err := parseAgent[AnalysisOut](bad); err == nil {
		t.Fatal("bad point status should fail validation")
	}
	dup := `{"summary":"s","solution":{"title":"x"},"points":[{"key":"P1","topic":"t","status":"open"},{"key":"P1","topic":"u","status":"open"}]}`
	if _, err := parseAgent[AnalysisOut](dup); err == nil {
		t.Fatal("duplicate point keys should fail validation")
	}
}

type client struct {
	t *testing.T
	h http.Handler
}

func (c client) call(method, url, body string, headers ...string) (int, map[string]any) {
	c.t.Helper()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// post sends a write with the required header.
func (c client) post(path, body string) map[string]any {
	c.t.Helper()
	code, out := c.call("POST", "http://127.0.0.1"+path, body, "X-Ledger", "1")
	if code != 200 {
		c.t.Fatalf("POST %s: %d %v", path, code, out)
	}
	return out
}

// finished waits for the background job that a POST started and returns its result.
func (c client) finished(started map[string]any) map[string]any {
	c.t.Helper()
	id, _ := started["job_id"].(string)
	for range 600 {
		code, out := c.call("GET", "http://127.0.0.1/api/jobs/"+id, "")
		if code != 200 {
			c.t.Fatalf("job %s: %d %v", id, code, out)
		}
		switch out["state"] {
		case "running":
			time.Sleep(50 * time.Millisecond)
		case "done":
			res, _ := out["result"].(map[string]any)
			return res
		default:
			c.t.Fatalf("job %s: %v", id, out)
		}
	}
	c.t.Fatal("job did not finish")
	return nil
}

func TestEndToEndWithFakeAgent(t *testing.T) {
	l := newLedger(t)
	t.Setenv("DLG_FAKE_AGENT", "1")
	fake := "'" + os.Args[0] + "'"
	_, err := l.updateSettings(D{"primary_name": "fake", "primary_command": fake, "implement_command": fake,
		"workdir": t.TempDir(), "secondary_names": []string{"gemini", "cursor"}, "secondary_with_code": []string{"cursor"}}, "user")
	must(t, err)
	s := &server{l}
	c := client{t, newServer(l)}

	if code, _ := c.call("POST", "http://127.0.0.1/api/problems", `{"title":"slow query"}`); code != 403 {
		t.Fatalf("no X-Ledger header: %d", code)
	}
	if code, _ := c.call("GET", "http://evil.com/api/state", ""); code != 403 {
		t.Fatalf("foreign host: %d", code)
	}

	pid := int64(c.post("/api/problems", `{"title":"slow query"}`)["id"].(float64))
	base := fmt.Sprintf("/api/problems/%d", pid)
	if got := c.finished(c.post(base+"/analyse", "")); got["round"] != 1.0 {
		t.Fatal(got)
	}
	detail := func() *Detail {
		d, err := s.detailWithHandoffs(pid)
		must(t, err)
		return d
	}
	d := detail()
	if len(d.Children) != 0 || len(d.Alternatives) != 0 {
		t.Fatal("analysis must only suggest subproblems and alternatives")
	}
	if h := d.Handoffs[0]; h.To != "gemini" || h.State != "unsent" || h.Mode != "full" || !strings.Contains(h.Text, "[P1] cache or index") {
		t.Fatalf("%+v", h)
	}
	// A reviewer with the repository open is pointed at the code instead of being given quotes.
	if gem, cur := d.Handoffs[0].Text, d.Handoffs[1].Text; !strings.Contains(gem, "> x = 1") || strings.Contains(gem, codeAccessNote) ||
		!strings.Contains(cur, codeAccessNote) || !strings.Contains(cur, "see a.py:1") || strings.Contains(cur, "> x = 1") {
		t.Fatalf("gemini:\n%s\n\ncursor:\n%s", gem, cur)
	}
	if code, _ := c.call("POST", "http://127.0.0.1"+base+"/handoffs", `{"to":"nobody"}`, "X-Ledger", "1"); code != 400 {
		t.Fatalf("unknown secondary: %d", code)
	}
	c.post(base+"/handoffs", `{"to":"gemini"}`)
	if h := detail().Handoffs[0]; h.State != "sent" {
		t.Fatal(h.State)
	}

	raw := "SUMMARY: cache it\n[P1] DISAGREE: a cache is simpler"
	rb, _ := json.Marshal(D{"author": "gemini", "body": raw})
	c.post(base+"/reviews", string(rb))
	if h := detail().Handoffs[0]; h.State != "replied" {
		t.Fatal(h.State)
	}
	c.post(base+"/notes", `{"body":"Q: how big? A: 2M rows"}`)
	if got := c.finished(c.post(base+"/analyse", "")); got["round"] != 2.0 || got["replies"] != 1.0 {
		t.Fatal(got)
	}
	d = detail()
	for _, x := range d.Contributions {
		if x.str("kind") == "review" && x.str("body") != raw {
			t.Fatal("review not kept verbatim")
		}
	}
	// The next copy is a delta: the change and the new note, not the whole context again.
	h := d.Handoffs[0]
	if h.State != "changed" || h.Mode != "delta" || !strings.Contains(h.Text, "recorded gemini's view") ||
		!strings.Contains(h.Text, "2M rows") || strings.Contains(h.Text, "## Context") {
		t.Fatalf("%+v", h)
	}

	if got := c.finished(c.post(base+"/finalise", "")); got["selected"] != true {
		t.Fatal(got)
	}
	d = detail()
	if d.Status != "selected" || len(d.Alternatives) != 1 || d.Alternatives[0].str("author") != "consensus" {
		t.Fatal(d.Status, d.Alternatives)
	}

	c.post(base+"/approve", "{}")
	// Regenerating after approval adds the proposal but keeps the approval.
	if got := c.finished(c.post(base+"/finalise", "")); got["selected"] != false {
		t.Fatal(got)
	}
	if d = detail(); d.Status != "approved" || len(d.Alternatives) != 2 {
		t.Fatal(d.Status, d.Alternatives)
	}
	if code, _ := c.call("GET", "http://127.0.0.1/api/models", ""); code != 403 {
		t.Fatalf("models without X-Ledger: %d", code)
	}
	if out, _ := c.finished(c.post("/api/implement", fmt.Sprintf(`{"problem_ids":[%d]}`, pid)))["output"].(string); strings.TrimSpace(out) != "implemented" {
		t.Fatal(out)
	}
	d = detail()
	if d.Status != "sent" {
		t.Fatal(d.Status)
	}
}

// analysed returns a ledger with one problem that the fake agent has analysed.
func analysed(t *testing.T) (*Ledger, int64) {
	t.Helper()
	l := newLedger(t)
	t.Setenv("DLG_FAKE_AGENT", "1")
	fake := "'" + os.Args[0] + "'"
	_, err := l.updateSettings(D{"primary_name": "fake", "primary_command": fake, "workdir": t.TempDir(),
		"secondary_names": []string{"gemini", "cursor"}}, "user")
	must(t, err)
	pid, err := l.createProblem("slow query", "", nil, "user")
	must(t, err)
	_, err = (&server{l}).analyse(pid)
	must(t, err)
	return l, pid
}

func sameJSON(t *testing.T, what string, a, b any) {
	t.Helper()
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if string(x) != string(y) {
		t.Fatalf("%s differs between SQLite and HTTP:\n%s\n%s", what, x, y)
	}
}

// The CLI may use either transport, so both must give the same answers.
func TestTransportsAgree(t *testing.T) {
	l, pid := analysed(t)
	ts := httptest.NewServer(newServer(l))
	defer ts.Close()
	var local Core = localCore{l}
	var remote Core = remoteCore{base: ts.URL, client: ts.Client()}

	sameProblems := func() {
		a, err := local.Problems()
		must(t, err)
		b, err := remote.Problems()
		must(t, err)
		sameJSON(t, "problems", a, b)
	}
	sameProblems()
	a, err := local.Handoff(pid, "gemini", false)
	must(t, err)
	b, err := remote.Handoff(pid, "gemini", false)
	must(t, err)
	sameJSON(t, "handoff", a, b)
	if _, err := remote.Handoff(pid, "nobody", false); err == nil {
		t.Fatal("unknown reviewer accepted")
	} else {
		wantLedgerError(t, err)
	}

	_, err = remote.Handoff(pid, "gemini", true) // record the copy
	must(t, err)
	reply := "SUMMARY: cache it\n[P1] DISAGREE: a cache is simpler\n  and cheaper\nOPEN: how big?"
	rv, err := remote.Review(pid, "gemini", reply, nil)
	must(t, err)
	if len(rv.Warnings) != 0 {
		t.Fatalf("warnings: %v", rv.Warnings)
	}
	sa, err := local.Status(pid)
	must(t, err)
	sb, err := remote.Status(pid)
	must(t, err)
	sameJSON(t, "status", sa, sb)
	if sa.NewReplies != 1 || len(sa.Waiting) != 0 || len(sa.ToSend) != 1 || sa.ToSend[0] != "cursor" {
		t.Fatalf("%+v", sa)
	}

	pa, err := local.Parse(rv.ID)
	must(t, err)
	pb, err := remote.Parse(rv.ID)
	must(t, err)
	sameJSON(t, "parse", pa, pb)
	if len(pa.Points) != 1 || pa.Points[0].Key != "P1" || pa.Points[0].Stance != "DISAGREE" ||
		pa.Points[0].Reason != "a cache is simpler\nand cheaper" || pa.Open != "how big?" {
		t.Fatalf("%+v", pa)
	}
	if _, err := local.Parse(pid); err == nil {
		t.Fatal("a problem id is not a review id")
	}
	sameJSON(t, "settings", must2(local.Settings()), must2(remote.Settings()))

	// The reply is kept verbatim; short or unformatted replies get warnings but are stored.
	odd, err := local.Review(pid, "gemini", "I think so,", nil)
	must(t, err)
	if len(odd.Warnings) != 2 {
		t.Fatalf("warnings: %v", odd.Warnings)
	}
	if _, err := local.Review(pid, "gemini", "  \n", nil); err == nil {
		t.Fatal("empty reply accepted")
	}
	if _, err := remote.Note(pid, ""); err == nil {
		t.Fatal("empty note accepted")
	}
}

func must2[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestRunnerCommandsAreCLIOnly(t *testing.T) {
	l := newLedger(t)
	c := client{t, newServer(l)}
	for _, key := range cliOnlySettings {
		if code, _ := c.call("PUT", "http://127.0.0.1/api/settings", `{"`+key+`": {}}`, "X-Ledger", "1"); code != 400 {
			t.Fatalf("%s over HTTP: %d", key, code)
		}
	}
	_, err := l.updateSettings(D{"secondary_commands": map[string]string{"gemini": "gemini -p"}}, "user")
	must(t, err)
	if s, _ := l.settings(); s.SecondaryCommands["gemini"] != "gemini -p" {
		t.Fatal(s.SecondaryCommands)
	}
}

// configProject makes a project in a temp dir, cds into it, and gives stdin a closed pipe (not a terminal).
func configProject(t *testing.T) *Ledger {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("DLG_CONFIG", filepath.Join(dir, "global.json"))
	must(t, os.MkdirAll(filepath.Join(dir, ledgerDir), 0o755))
	l, err := openLedger(dbPath(dir))
	must(t, err)
	t.Cleanup(func() { l.close() })
	r, w, err := os.Pipe()
	must(t, err)
	w.Close()
	stdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = stdin
		r.Close()
	})
	return l
}

func TestCommandSettersWithoutTerminal(t *testing.T) {
	l := configProject(t)
	before, err := l.settings()
	must(t, err)
	for _, args := range [][]string{{"use", "claude"}, {"set-primary", "claude", "claude -p"}, {"set-workdir", t.TempDir()}} {
		if err := cmdConfig(args); err == nil {
			t.Fatalf("%v without --yes: no error", args)
		}
	}
	if err := cmdConfig([]string{"use", "codex", "--yes"}); !errors.As(err, new(usageError)) {
		t.Fatalf("trailing --yes: %v", err)
	}
	if s, _ := l.settings(); s.PrimaryName != before.PrimaryName || s.PrimaryCommand != before.PrimaryCommand ||
		s.ImplementCommand != before.ImplementCommand || s.Workdir != before.Workdir {
		t.Fatalf("stored without --yes: %+v", s)
	}
	must(t, cmdConfig([]string{"use", "--yes", "claude"}))
	if s, _ := l.settings(); s.PrimaryName != "claude" || s.PrimaryCommand != "claude -p" || s.ImplementCommand != "claude -p --permission-mode acceptEdits" {
		t.Fatalf("use --yes: %+v", s)
	}
	must(t, cmdConfig([]string{"set-primary", "--yes", "x", "x -p"}))
	if s, _ := l.settings(); s.PrimaryCommand != "x -p" || s.ImplementCommand != "claude -p --permission-mode acceptEdits" {
		t.Fatalf("set-primary without implement: %+v", s)
	}

	_, err = l.updateSettings(D{"runner_confirmed": []string{"other"}}, "user")
	must(t, err)
	for _, c := range []struct {
		args          []string
		agent, stored string
	}{
		{[]string{"set-runner", "claude", "claude", "-p"}, "claude", "claude -p"},
		{[]string{"set-runner", "--yes", "claude", "claude", "-p"}, "claude", "claude -p"},
		{[]string{"set-runner", "--yes", "codex"}, "codex", "codex exec"},
		{[]string{"set-runner", "codex", "codex", "exec", "--full-auto"}, "codex", "codex exec --full-auto"},
	} {
		must(t, cmdConfig(c.args))
		s, _ := l.settings()
		if s.SecondaryCommands[c.agent] != c.stored || len(s.RunnerConfirmed) != 1 || s.RunnerConfirmed[0] != "other" {
			t.Fatalf("%v: %q %v", c.args, s.SecondaryCommands[c.agent], s.RunnerConfirmed)
		}
	}
	for _, args := range [][]string{{"set-runner", "codex", "--yes"}, {"set-runner", "gemini"}} {
		if err := cmdConfig(args); err == nil {
			t.Fatalf("%v: no error", args)
		}
	}
	if s, _ := l.settings(); s.SecondaryCommands["codex"] != "codex exec --full-auto" || s.SecondaryCommands["gemini"] != "" {
		t.Fatalf("refused runner stored: %v", s.SecondaryCommands)
	}
}

func TestGlobalSettersRefuseStrayYes(t *testing.T) {
	configProject(t)
	for _, args := range [][]string{{"set-runner", "codex", "--yes"}, {"set-primary", "claude", "claude -p", "--yes"},
		{"set-runner", "--yes", "codex"}, {"set-primary", "--yes", "claude", "claude -p"}} {
		if err := cmdConfig(append([]string{"--global"}, args...)); err == nil {
			t.Fatalf("--global %v: no error", args)
		}
	}
	if raw, err := readGlobalConfig(); err != nil || len(raw) != 0 {
		t.Fatalf("global config written: %v %v", raw, err)
	}
}

// The UI may start a runner the CLI set and approved, but never an unapproved one.
func TestRunReviewerFromUI(t *testing.T) {
	l, pid := analysed(t)
	c := client{t, newServer(l)}
	url := fmt.Sprintf("http://127.0.0.1/api/problems/%d/runs/gemini", pid)
	if code, _ := c.call("POST", url, "", "X-Ledger", "1"); code != 400 {
		t.Fatalf("no runner: %d", code)
	}
	fake := "'" + os.Args[0] + "'"
	_, err := l.updateSettings(D{"secondary_commands": map[string]string{"gemini": fake}}, "user")
	must(t, err)
	if code, out := c.call("POST", url, "", "X-Ledger", "1"); code != 400 {
		t.Fatalf("unapproved runner started: %d %v", code, out)
	}
	_, err = l.updateSettings(D{"runner_confirmed": []string{fake}}, "user")
	must(t, err)
	t.Setenv("DLG_FAKE_AGENT", "stdin")
	res := c.finished(c.post(fmt.Sprintf("/api/problems/%d/runs/gemini", pid), ""))
	if res["review_id"] == nil {
		t.Fatalf("%v", res)
	}
	if s := must2(localCore{l}.Status(pid)); s.NewReplies != 1 {
		t.Fatalf("%+v", s)
	}
}

// Auto rounds run approved runners, process their replies, and hand back to the user on questions or copy-paste.
func TestCycleRounds(t *testing.T) {
	l, pid := analysed(t)
	c := client{t, newServer(l)}
	path := fmt.Sprintf("/api/problems/%d/cycle", pid)
	if code, _ := c.call("POST", "http://127.0.0.1"+path, `{"rounds":2}`, "X-Ledger", "1"); code != 400 {
		t.Fatalf("no approved runner: %d", code)
	}
	runner := "env DLG_FAKE_AGENT=stdin '" + os.Args[0] + "'"
	_, err := l.updateSettings(D{"secondary_commands": map[string]string{"gemini": runner}}, "user")
	must(t, err)
	if code, _ := c.call("POST", "http://127.0.0.1"+path, `{"rounds":2}`, "X-Ledger", "1"); code != 400 {
		t.Fatalf("unapproved runner: %d", code)
	}
	_, err = l.updateSettings(D{"runner_confirmed": []string{runner}}, "user")
	must(t, err)
	if code, _ := c.call("POST", "http://127.0.0.1"+path, `{"rounds":4}`, "X-Ledger", "1"); code != 400 {
		t.Fatalf("too many rounds: %d", code)
	}
	// cursor has no runner: gemini reviews, then the loop waits for cursor's paste.
	res := c.finished(c.post(path, `{"rounds":2}`))
	if why, _ := res["stopped"].(string); !strings.Contains(why, "cursor") {
		t.Fatalf("%v", res)
	}
	if s := must2(localCore{l}.Status(pid)); s.NewReplies != 1 || s.Round != 1 {
		t.Fatalf("%+v", s)
	}
	// With only runners, the reply is processed; the fake primary always asks a question, so it pauses there.
	_, err = l.updateSettings(D{"secondary_names": []string{"gemini"}}, "user")
	must(t, err)
	started := c.post(path, `{"rounds":2}`)
	res = c.finished(started)
	if why, _ := res["stopped"].(string); !strings.Contains(why, "question") {
		t.Fatalf("%v", res)
	}
	// The job keeps the last position it reported, for the UI's stats strip.
	_, j := c.call("GET", "http://127.0.0.1/api/jobs/"+started["job_id"].(string), "")
	if p, _ := j["progress"].(map[string]any); p["phase"] != "processing" || p["round"] != 1.0 || p["rounds"] != 2.0 {
		t.Fatalf("progress %v", j["progress"])
	}
	if s := must2(localCore{l}.Status(pid)); s.NewReplies != 0 || s.Round != 2 {
		t.Fatalf("%+v", s)
	}
}

// A last event without a trailing newline still counts once flushed.
func TestStreamReaderFlush(t *testing.T) {
	s := &streamReader{progress: io.Discard}
	s.Write([]byte(`{"type":"result","result":"late","is_error":false}`))
	s.flush()
	if s.result == nil || *s.result != "late" {
		t.Fatalf("unflushed result %v", s.result)
	}
}

// The agent's own "decided" is not a user ruling: it neither settles a point nor reaches the implementer.
func TestDecidedNeedsRuling(t *testing.T) {
	a := &AnalysisOut{Points: []PointOut{{Key: "P1", Topic: "t", Primary: "x", Status: "decided"}, {Key: "P2", Topic: "u", Primary: "y", Status: "agreed"}}}
	d := &Detail{}
	if lines := decisionLines(d, a); len(lines) != 1 || !strings.Contains(lines[0], "[P2]") || !strings.Contains(lines[0], "no user ruling") {
		t.Fatal(lines)
	}
	if settled(a.Points[0], rulings(d)) || !settled(a.Points[0], map[string]string{"P1": "go with x"}) {
		t.Fatal("decided must need a ruling note")
	}
	bad := `{"summary":"s","solution":{"title":"x"},"points":[{"key":"\"><img src=x>","topic":"t","status":"open"}]}`
	if _, err := parseAgent[AnalysisOut](bad); err == nil {
		t.Fatal("unsafe point key should fail validation")
	}
}

// stream-json events become readable progress; only the result is kept, and full repeats of streamed text are not shown twice.
func TestStreamReader(t *testing.T) {
	var out strings.Builder
	s := &streamReader{progress: &out}
	for _, l := range []string{
		`{"type":"thinking","subtype":"delta","text":"x"}`,
		`{"type":"assistant","message":{"content":[{"text":"I'll "}]}}`,
		`{"type":"assistant","message":{"content":[{"text":"look"}]}}`,
		`{"type":"assistant","message":{"content":[{"text":"I'll look"}]}}`,
		`{"type":"tool_call","subtype":"started","tool_call":{"shellToolCall":{"args":{"command":"ls -la"}}}}`,
		`{"type":"user"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"done"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"app.go"}}]}}`,
		`{"type":"result","subtype":"success","result":"SUMMARY: ok","is_error":false}`,
	} {
		s.Write([]byte(l + "\n"))
	}
	if s.result == nil || *s.result != "SUMMARY: ok" {
		t.Fatalf("result %v", s.result)
	}
	if got := out.String(); strings.Count(got, "I'll look") != 1 || !strings.Contains(got, "🔧 shell: ls -la") || !strings.Contains(got, "thinking") ||
		strings.Count(got, "done") != 1 || !strings.Contains(got, "🔧 Read: app.go") {
		t.Fatalf("%q", got)
	}
}

func TestWithStreaming(t *testing.T) {
	for in, want := range map[string]string{
		"claude -p":                            "claude -p --output-format stream-json --verbose --include-partial-messages",
		"/x/cursor-agent -p --mode ask":        "/x/cursor-agent -p --mode ask --output-format stream-json --stream-partial-output",
		"claude -p --output-format text":       "claude -p --output-format text", // the user's choice wins
		"claude -p --output-format=json":       "claude -p --output-format=json",
		"claude --permission-mode acceptEdits": "claude --permission-mode acceptEdits", // not print mode
		"codex exec":                           "codex exec",
	} {
		args, _ := splitCommand(in)
		if got := strings.Join(withStreaming(args), " "); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestExecRunner(t *testing.T) {
	fake := "'" + os.Args[0] + "'"
	t.Setenv("DLG_FAKE_AGENT", "stdin")
	res, err := execRunner(context.Background(), fake, "hello", t.TempDir(), 30*time.Second, io.Discard)
	must(t, err)
	if !strings.Contains(res.stdout, "read 5 bytes") || res.exitCode != 0 || res.truncated {
		t.Fatalf("%+v", res)
	}
	t.Setenv("DLG_FAKE_AGENT", "empty")
	if _, err := execRunner(context.Background(), fake, "hello", t.TempDir(), 30*time.Second, io.Discard); err == nil {
		t.Fatal("empty output must not be stored")
	}
	if _, err := execRunner(context.Background(), "dlg-no-such-command-x", "", t.TempDir(), time.Second, io.Discard); err == nil {
		t.Fatal("missing command")
	}
	w := &capWriter{max: 4}
	w.Write([]byte("abc"))
	w.Write([]byte("def"))
	if w.buf.String() != "abcd" || !w.truncated {
		t.Fatalf("%q %v", w.buf.String(), w.truncated)
	}
}

func TestMigrationAddsMetaColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	l, err := openLedger(path)
	must(t, err)
	_, err = l.db.Exec("ALTER TABLE contributions DROP COLUMN meta") // a ledger from before the column existed
	must(t, err)
	l.close()
	l, err = openLedger(path)
	must(t, err)
	defer l.close()
	pid, _ := l.createProblem("p", "", nil, "user")
	_, err = l.addContributionMeta(pid, "x", "review", "body", D{"truncated": true})
	must(t, err)
}

// Conformance of the stdio server: one response per request, none for notifications.
func TestMCPConformance(t *testing.T) {
	l, pid := analysed(t)
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":"x","method":"tools/list"}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"dlg_handoff","arguments":{"problem_id":%d,"to":"gemini","peek":true}}}`, pid),
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"dlg_status","arguments":{"problem_id":999}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"nope"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"nope"}`,
		`not json`,
	}, "\n") + "\n"
	var out strings.Builder
	must(t, serveMCP(strings.NewReader(in), &out, localCore{l}))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 8 {
		t.Fatalf("want 8 responses, got %d:\n%s", len(lines), out.String())
	}
	var got []map[string]any
	for _, line := range lines {
		var m map[string]any
		must(t, json.Unmarshal([]byte(line), &m))
		if m["jsonrpc"] != "2.0" {
			t.Fatalf("not JSON-RPC 2.0: %s", line)
		}
		got = append(got, m)
	}
	result := func(i int) map[string]any { return got[i]["result"].(map[string]any) }
	code := func(i int) float64 { return got[i]["error"].(map[string]any)["code"].(float64) }
	if result(0)["protocolVersion"] != "2024-11-05" || result(0)["serverInfo"].(map[string]any)["name"] != "dlg" {
		t.Fatalf("initialize: %v", got[0])
	}
	if got[1]["id"] != 2.0 || got[2]["id"] != "x" || len(result(2)["tools"].([]any)) != len(mcpTools) {
		t.Fatalf("ids or tools: %v %v", got[1], got[2])
	}
	for _, tool := range result(2)["tools"].([]any) {
		if tool.(map[string]any)["inputSchema"].(map[string]any)["type"] != "object" {
			t.Fatalf("tool without an object schema: %v", tool)
		}
	}
	text := result(3)["content"].([]any)[0].(map[string]any)["text"].(string)
	if result(3)["isError"] != false || !strings.Contains(text, "[P1] cache or index") {
		t.Fatalf("handoff: %v", got[3])
	}
	if result(4)["isError"] != true { // a failing tool is a result, not a protocol error
		t.Fatalf("status of a missing problem: %v", got[4])
	}
	if code(5) != -32602 || code(6) != -32601 || code(7) != -32700 || got[7]["id"] != nil {
		t.Fatalf("errors: %v %v %v", got[5], got[6], got[7])
	}
}

// withVerifiedTemplates marks every template entry verified for the test, then restores them.
func withVerifiedTemplates(t *testing.T) {
	orig := agentTemplates
	agentTemplates = map[string]agentTmpl{}
	for a, at := range orig {
		at.Model.Verified, at.Effort.Verified = true, true
		cs := map[string]ctxTmpl{}
		for k, c := range at.Contexts {
			c.Verified = true
			cs[k] = c
		}
		at.Contexts = cs
		agentTemplates[a] = at
	}
	t.Cleanup(func() { agentTemplates = orig })
}

func TestModelTemplates(t *testing.T) {
	withVerifiedTemplates(t)
	for _, c := range []struct {
		agent, want string
		m           ModelPreset
	}{
		{"claude", "--model opus[1m] --effort high", ModelPreset{Model: "opus", Context: "1m", Effort: "high"}},
		{"codex", "-m gpt-5 -c model_context_window=200000 -c model_reasoning_effort=low", ModelPreset{Model: "gpt-5", Context: "200k", Effort: "low"}},
	} {
		if got, err := renderArgs(c.agent, c.m); err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.agent, got, err, c.want)
		}
	}
	if _, err := renderArgs("claude", ModelPreset{Model: "opus", Context: "9m"}); err == nil {
		t.Error("unknown context accepted")
	}
	if _, err := renderArgs("gemini", ModelPreset{Model: "x"}); err == nil {
		t.Error("agent without templates accepted")
	}
}

func TestAddModelValidation(t *testing.T) {
	withVerifiedTemplates(t)
	if w, err := validatePreset("claude", ModelPreset{Model: "opus", Context: "1m", Effort: "high"}); err != nil || len(w) != 0 {
		t.Errorf("known combination: %v, %v", w, err)
	}
	if w, err := validatePreset("claude", ModelPreset{Model: "opus", Effort: "extreme"}); err != nil || len(w) != 1 {
		t.Errorf("unknown effort should warn, not fail: %v, %v", w, err)
	}
	if w, err := validatePreset("claude", ModelPreset{Model: "mystery"}); err != nil || len(w) != 1 {
		t.Errorf("unknown model should warn, not fail: %v, %v", w, err)
	}
	m, err := addModelPreset([]string{"k", "K", "x", "--model", "opus", "--context", "1m", "--effort", "low", "-p"})
	if err != nil || m.Model != "opus" || m.Context != "1m" || m.Effort != "low" || m.Args != "-p" || m.ContextLabel != "1m" {
		t.Errorf("parsed %+v, %v", m, err)
	}
	if m, _ = addModelPreset([]string{"k", "K", "x", "--", "--model", "raw"}); m.Model != "" || m.Args != "--model raw" {
		t.Errorf("-- must pass raw args: %+v", m)
	}
}

// P14: a preset with only Args skips rendering, even while no template is verified.
func TestLegacyArgsPassThrough(t *testing.T) {
	cfg := defaultSettings()
	cfg.PrimaryCommand = "claude -p"
	cfg.Models = []ModelPreset{{Key: "old", Label: "Old", Args: "--model x"}}
	if cmd, _, err := resolveModel(cfg, "old", false); err != nil || cmd != "claude -p --model x" {
		t.Errorf("got %q, %v", cmd, err)
	}
}

func templatesVerified() bool {
	for _, a := range agentTemplates {
		ok := a.Model.Verified && a.Effort.Verified
		for _, c := range a.Contexts {
			ok = ok && c.Verified
		}
		if !ok {
			return false
		}
	}
	return true
}

// P15: unverified entries render nothing and are refused by add-model, init-models and runs.
func TestUnverifiedEntriesAreRefused(t *testing.T) {
	if templatesVerified() {
		t.Skip("all templates verified")
	}
	m := ModelPreset{Key: "u", Model: "gpt-5", Context: "200k", Effort: "high"} // codex is still unverified
	if got, err := renderArgs("codex", m); err == nil || got != "" {
		t.Errorf("rendered %q, %v", got, err)
	}
	if _, err := validatePreset("codex", m); err == nil {
		t.Error("add-model accepted an unverified entry")
	}
	if seeded, _ := seedPresets("codex", "1"); len(seeded) != 0 {
		t.Error("init-models seeded unverified entries")
	}
	cfg := defaultSettings()
	cfg.PrimaryCommand = "codex exec"
	cfg.Models = []ModelPreset{m}
	if _, _, err := resolveModel(cfg, "u", false); err == nil {
		t.Error("a run resolved an unverified preset")
	}
}

// Release gate: set DLG_RELEASE=1 in the release job to fail while any flag literal is still
// unverified against the installed claude and codex CLIs.
func TestTemplatesVerified(t *testing.T) {
	if os.Getenv("DLG_RELEASE") == "" {
		t.Skip("release gate: set DLG_RELEASE=1")
	}
	if !templatesVerified() {
		t.Fatal("agentTemplates has unverified entries; verify the flag literals against the installed CLIs")
	}
}

func TestGlobalConfigLayersUnderProjectSettings(t *testing.T) {
	t.Setenv("DLG_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	must(t, cmdGlobalConfig([]string{"set-primary", "claude", "claude -p"}))
	must(t, cmdGlobalConfig([]string{"set-runner", "cursor", "cursor-agent -p"}))
	l := newLedger(t)
	s, err := l.settings()
	must(t, err)
	if s.PrimaryName != "claude" || s.SecondaryCommands["cursor"] != "cursor-agent -p" {
		t.Fatalf("global not applied: %+v", s)
	}
	_, err = l.updateSettings(D{"primary_name": "codex"}, "user")
	must(t, err)
	if s, _ = l.settings(); s.PrimaryName != "codex" || s.PrimaryCommand != "claude -p" {
		t.Fatalf("local should win per key: %+v", s)
	}
}

// Reviewers get only open and disputed points; their own rewritten position does not count
// as a change, and points settled since the last handoff are listed by key.
func TestHandoffSendsOnlyPointsForReview(t *testing.T) {
	ruling := "Ruling on [P3] Logging: go with claude (defer)"
	d := &Detail{Problem: Row{"title": "x", "context": "c"}, Contributions: []Row{{"id": int64(1), "kind": "context", "body": ruling}}}
	pt := func(key, status, cursor string) PointOut {
		return PointOut{Key: key, Topic: "T" + key, Primary: "p", Status: status, Positions: map[string]string{"cursor": cursor}}
	}
	prev := &AnalysisOut{Points: []PointOut{pt("P1", "open", "a"), pt("P2", "open", "a"), pt("P3", "open", "a"), pt("P4", "agreed", "a")}}
	cur := &AnalysisOut{Points: []PointOut{pt("P1", "open", "a; round 2: AGREE"), pt("P2", "disputed", "b"), pt("P3", "open", "a"), pt("P4", "agreed", "a"), pt("P5", "open", "")}}

	full := fullHandoff(d, cur, "claude", "cursor", false, 2, nil)
	if !strings.Contains(full, "[P1] TP1 (open)\n") || !strings.Contains(full, "[P3] TP3 (user ruling)") || !strings.Contains(full, "[P4] TP4 (agreed)") || strings.Contains(full, "[P4] TP4 (agreed)\n  claude") {
		t.Fatal(full)
	}
	delta := deltaHandoff(d, prev, cur, "claude", "cursor", false, 2, []string{ruling}, nil)
	review, settledPart, _ := strings.Cut(delta, "Newly settled")
	if strings.Contains(review, "[P1]") || !strings.Contains(review, "[P2]") || !strings.Contains(review, "[P5]") || strings.Contains(review, "[P4]") {
		t.Fatal(delta)
	}
	if !strings.Contains(settledPart, "[P3] TP3 (user ruling)") || strings.Contains(settledPart, "[P4]") {
		t.Fatal(delta)
	}
	// Once the ruling was seen, P3 is not listed again.
	if again := deltaHandoff(d, cur, cur, "claude", "cursor", false, 3, nil, nil); strings.Contains(again, "[P3]") || !strings.Contains(again, "No points need your review") {
		t.Fatal(again)
	}
}

// Claude Code's result event carries cost, tokens and models; cursor-agent's (shape assumed,
// not checked against a live run) carries no cost, which must stay "not reported", not $0.
func TestUsageFromResultEvents(t *testing.T) {
	claude := `{"type":"result","is_error":false,"result":"ok","duration_ms":46000,"duration_api_ms":40000,"num_turns":12,"session_id":"s1","total_cost_usd":0.84,"usage":{"input_tokens":10,"output_tokens":800,"cache_read_input_tokens":3000,"cache_creation_input_tokens":200},"modelUsage":{"claude-opus-5-5":{},"claude-haiku-5-5":{}}}`
	s := &streamReader{progress: io.Discard}
	s.Write([]byte(claude + "\n"))
	u := s.usage
	if u == nil || u.CostUSD == nil || *u.CostUSD != 0.84 || u.OutputTokens != 800 || u.CacheRead != 3000 || u.Turns != 12 ||
		strings.Join(u.Models, ",") != "claude-haiku-5-5,claude-opus-5-5" || u.SessionID != "s1" {
		t.Fatalf("%+v", u)
	}
	cursor := parseUsage([]byte(`{"type":"result","result":"ok","duration_ms":1234,"sessionId":"c1"}`))
	if cursor.CostUSD != nil || cursor.DurationMS != 1234 || cursor.SessionID != "c1" {
		t.Fatalf("%+v", cursor)
	}

	cost := func(c float64) *float64 { return &c }
	ev := func(actor string, m D) Row {
		b, _ := json.Marshal(m)
		return Row{"action": "usage", "actor": actor, "detail": string(b)}
	}
	d := &Detail{Events: []Row{
		ev("claude", usageMeta(&Usage{CostUSD: cost(1), InputTokens: 100, Models: []string{"opus"}}, "analyse", presetText(&ModelPreset{Key: "o1m", Model: "opus", Context: "1m"}), false)),
		ev("claude", usageMeta(&Usage{CostUSD: cost(0.5), Models: []string{"opus"}}, "analyse", presetText(&ModelPreset{Key: "o1m", Model: "opus", Context: "1m"}), true)),
		ev("claude", D{"usage": &Usage{CostUSD: cost(3), InputTokens: 300}, "op": "implement", "shared": 3}),
		ev("cursor", usageMeta(cursor, "review", runnerModel("cursor-agent -p --model gpt-5.5-high"), false)),
		{"action": "run_analyse", "actor": "user", "detail": "{}"},
	}}
	c := costOf(d)
	if c.Total.Runs != 4 || c.Total.Priced != 3 || c.Total.Failed != 1 || c.Total.CostUSD != 2.5 || c.Total.InputTokens != 200 {
		t.Fatalf("%+v", c.Total)
	}
	if len(c.ByAgent) != 2 || c.ByAgent[1].Name != "cursor" || c.ByAgent[1].Priced != 0 || costText(c.ByAgent[1]) != "no cost reported" {
		t.Fatalf("%+v", c.ByAgent)
	}
	names := []string{}
	for _, l := range c.ByModel {
		names = append(names, l.Name)
	}
	if fmt.Sprint(names) != "[gpt-5.5-high o1m (opus, 1m) → opus unknown]" {
		t.Fatal(names)
	}
	if costText(c.Total) != "$2.50 (+1 run(s) without a cost)" {
		t.Fatal(costText(c.Total))
	}
	if costOf(&Detail{}) != nil {
		t.Fatal("no usage should be nil")
	}
}

// A reviewer model comes only from the runner CLI's list and is one --model argument.
func TestReviewerModels(t *testing.T) {
	list := parseCursorModels("Available models\n\nauto - Auto (default)\n\x1b[1mgpt-5.5-high\x1b[0m - GPT-5.5 1M High\r\ngpt-5.3-codex - Codex 5.3\n--evil - x\n")
	if len(list) != 3 || list[1].Model != "gpt-5.5" || list[1].Effort != "high" || list[1].Context != "1m" || list[2].Effort != "" {
		t.Fatalf("%+v", list)
	}
	cfg := Settings{SecondaryCommands: map[string]string{"cursor": "cursor-agent -p", "fixed": "cursor-agent -p --model auto", "gemini": "gemini -p"},
		ReviewModels: map[string]string{"cursor": "gpt-5.3-codex"}}
	for _, c := range []struct{ agent, model, want, err string }{
		{"cursor", "gpt-5.5-high", "cursor-agent -p --model gpt-5.5-high", ""},
		{"cursor", "", "cursor-agent -p --model gpt-5.3-codex", ""}, // the saved default
		{"cursor", noModel, "cursor-agent -p", ""},
		{"cursor", "gpt-9", "", "not one of"},
		{"cursor", "x --force", "", "not one of"},
		{"fixed", "auto", "", "already sets --model"},
		{"gemini", "gpt-5.5-high", "", "cannot pick a model"},
		{"gemini", "", "gemini -p", ""},
	} {
		got, _, err := reviewerCommand(cfg, c.agent, c.model, list)
		if got != c.want || (c.err == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s %q: %q %v", c.agent, c.model, got, err)
		}
	}
	// With no list (the CLI could not be asked), only the saved default still runs.
	if _, _, err := reviewerCommand(cfg, "cursor", "", nil); err != nil {
		t.Error(err)
	}
	if _, _, err := reviewerCommand(cfg, "cursor", "gpt-5.5-high", nil); err == nil {
		t.Error("an unlisted model ran")
	}
	l := newLedger(t)
	if _, err := l.updateSettings(D{"review_models": map[string]string{"cursor": "--force"}}, "user"); err == nil {
		t.Error("a flag was saved as a model")
	}
}

// Auto rounds stop once no point is open or disputed, even if some still wait on the user.
func TestCycleStopsWhenNothingToReview(t *testing.T) {
	l, pid := analysed(t)
	s := &server{l}
	a := D{"summary": "s", "solution": D{"title": "t"}, "points": []D{{"key": "P1", "topic": "x", "primary": "y", "status": "decided"}}}
	b, _ := json.Marshal(a)
	_, err := l.addContribution(pid, "fake", "analysis", string(b))
	must(t, err)
	stop, why, err := s.cycleShouldStop(pid)
	if err != nil || !stop || !strings.Contains(why, "nothing left for reviewers; 1 point(s)") {
		t.Fatalf("%v %q %v", stop, why, err)
	}
}
