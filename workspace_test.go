package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstRunDoesNotConfigureAnAgent(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("DLG_CONFIG", filepath.Join(dir, "global.json"))
	must(t, initLedger())
	l, err := openLedger(dbPath(dir))
	must(t, err)
	defer l.close()
	info, err := setupInfo(l)
	must(t, err)
	if info["configured"] != false {
		t.Fatalf("silently configured: %v", info)
	}
	cfg, err := l.settings()
	must(t, err)
	if len(cfg.SecondaryNames) != 0 {
		t.Fatal("reviewers were added implicitly")
	}
}
func TestScopedModelsAndSameToolReviewers(t *testing.T) {
	t.Setenv("DLG_CONFIG", filepath.Join(t.TempDir(), "global.json"))
	must(t, writeGlobalConfig(D{"primary_name": "claude", "primary_command": "claude -p", "implement_command": "claude -p", "secondary_names": []string{"reviewer-1", "reviewer-2"}, "secondary_commands": map[string]string{"reviewer-1": "claude -p", "reviewer-2": "claude -p"}}))
	l := newLedger(t)
	c := client{t, newServer(l)}
	cfg, err := l.settings()
	must(t, err)
	list, msg, source := choiceViews(cfg, cfg.PrimaryCommand, false)
	if len(list) == 0 || msg != "" || !strings.Contains(source, "Built-in") {
		t.Fatalf("%v %s %s", list, msg, source)
	}
	key := list[0].Key
	request := func(scope string, values D) (int, map[string]any) {
		body, _ := json.Marshal(D{"scope": scope, "values": values})
		return c.call("PUT", "http://127.0.0.1/api/agent-preferences", string(body), "X-Ledger", "1")
	}
	if code, out := request("global", D{"default_analyse_model": key}); code != 200 {
		t.Fatal(code, out)
	}
	cfg, err = l.settings()
	must(t, err)
	if cfg.DefaultAnalyseModel != key {
		t.Fatal("global model not inherited")
	}
	for _, values := range []D{{"primary_command": "evil"}, {"models": []D{{"args": "--evil"}}}, {"default_analyse_model": "unknown --force"}} {
		if code, out := request("global", values); code != 400 {
			t.Fatal(code, out)
		}
	}
	_, err = l.updateSettings(D{"runner_confirmed": []string{"claude -p"}}, "user")
	must(t, err)
	for _, name := range cfg.SecondaryNames {
		code, out := request("project", D{"review_models": map[string]string{name: key}})
		if code != 200 {
			t.Fatal(code, out)
		}
		command, model, err := reviewerCommand(cfg, name, key, list)
		must(t, err)
		if !strings.Contains(command, "--model") || model == "" {
			t.Fatal(command, model)
		}
	}
	// One project can override a model without changing another project's inherited choice.
	if code, out := request("project", D{"default_analyse_model": "none"}); code != 200 {
		t.Fatal(code, out)
	}
	other := newLedger(t)
	otherCfg, err := other.settings()
	must(t, err)
	if otherCfg.DefaultAnalyseModel != key {
		t.Fatal("project write changed global default")
	}
	c.post("/api/settings/inherit", `{"group":"primary"}`)
	cfg, err = l.settings()
	must(t, err)
	if cfg.DefaultAnalyseModel != key {
		t.Fatal("global default not restored")
	}
}
func TestAnswersAreLinkedAndSurviveFailedAnalysis(t *testing.T) {
	l := newLedger(t)
	pid, err := l.createProblem("Question test", "", nil, "user")
	must(t, err)
	analysis := `{"summary":"s","solution":{"title":"t"},"questions":[{"question":"Platforms?","assumption":"Linux"}]}`
	aid, err := l.addContribution(pid, "fake", "analysis", analysis)
	must(t, err)
	c := client{t, newServer(l)}
	path := fmt.Sprintf("/api/problems/%d/answers", pid)
	body := fmt.Sprintf(`{"analysis_id":%d,"index":0,"answer":"Linux and macOS"}`, aid)
	c.post(path, body)
	c.post(path, body)
	d, err := l.problemDetail(pid)
	must(t, err)
	if len(ofKind(d, "context")) != 1 || unansweredQuestions(d) != 0 {
		t.Fatal("answer not durable / duplicate")
	}
	_, err = l.updateSettings(D{"primary_command": "dlg-intentionally-missing-agent"}, "user")
	must(t, err)
	if _, err := (&server{l}).analyse(pid); err == nil {
		t.Fatal("expected agent launch failure")
	}
	d, err = l.problemDetail(pid)
	must(t, err)
	if unansweredQuestions(d) != 0 || len(ofKind(d, "context")) != 1 {
		t.Fatal("failed analysis lost saved answer")
	}
	for _, bad := range []string{fmt.Sprintf(`{"analysis_id":%d,"index":1,"answer":"x"}`, aid), fmt.Sprintf(`{"analysis_id":%d,"index":0,"answer":""}`, aid)} {
		if code, out := c.call("POST", "http://127.0.0.1"+path, bad, "X-Ledger", "1"); code != 400 {
			t.Fatal(code, out)
		}
	}
	aid2, err := l.addContribution(pid, "fake", "analysis", analysis)
	must(t, err)
	d, err = l.problemDetail(pid)
	must(t, err)
	if unansweredQuestions(d) != 1 {
		t.Fatal("old answer linked to new question")
	}
	if code, _ := c.call("POST", "http://127.0.0.1"+path, body, "X-Ledger", "1"); code != 400 {
		t.Fatal("stale question accepted")
	}
	c.post(path, fmt.Sprintf(`{"analysis_id":%d,"index":0,"use_assumption":true}`, aid2))
	d, err = l.problemDetail(pid)
	must(t, err)
	if unansweredQuestions(d) != 0 {
		t.Fatal("explicit assumption not saved")
	}
}

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestUpdateNoticeAndSafeInstructions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("LOCALAPPDATA", dir)
	previousVersion, previousClient := version, releaseClient
	t.Cleanup(func() { version = previousVersion; releaseClient = previousClient })
	version = "v0.1.0"
	calls := 0
	releaseClient = &http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != releaseAPI {
			t.Fatal("unexpected update source", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"tag_name":"v0.2.0","assets":[{"name":%q,"browser_download_url":"https://evil.example/binary"},{"name":"SHA256SUMS"}]}`, releaseAsset())))}, nil
	})}
	c := client{t, newServer(newLedger(t))}
	if code, out := c.call("GET", "http://127.0.0.1/api/about", ""); code != 200 || out["current"] != "v0.1.0" {
		t.Fatal(code, out)
	}
	if calls != 0 {
		t.Fatal("About triggered an update check")
	}
	if code, _ := c.call("GET", "http://127.0.0.1/api/updates", ""); code != 403 {
		t.Fatal("unguarded update check")
	}
	u := checkUpdates(context.Background(), true)
	if !u.Available || u.Error != "" || strings.Contains(u.Commands, "evil.example") || !strings.Contains(u.Commands, "SHA256SUMS") {
		t.Fatalf("%+v", u)
	}
	_ = checkUpdates(context.Background(), false)
	if calls != 1 {
		t.Fatal("shared cache ignored")
	}
	version = "dev"
	u = checkUpdates(context.Background(), false)
	if u.Available || !u.Development {
		t.Fatal("dev build compared as a release")
	}
	if newerVersion("v1.0.0-evil", "v0.1.0") || newerVersion("v0.1.0", "v0.2.0") || !newerVersion("v0.10.0", "v0.2.0") {
		t.Fatal("incorrect version comparison")
	}
	if got := updateCommands("v1.0.0;evil", "/tmp/dlg"); got != "" {
		t.Fatal("unsafe tag accepted")
	}
	// Cache contains only release metadata, never project contents or credentials.
	data, err := os.ReadFile(updateCachePath())
	must(t, err)
	if !strings.Contains(string(data), "v0.2.0") {
		t.Fatal("cache missing")
	}
}

func TestPrimaryRunUsesCapturedConfiguration(t *testing.T) {
	l := newLedger(t)
	t.Setenv("DLG_FAKE_AGENT", "1")
	original := defaultSettings()
	original.PrimaryName = "original"
	original.PrimaryCommand = "'" + os.Args[0] + "'"
	original.Workdir = t.TempDir()
	_, err := l.updateSettings(D{"primary_name": "replacement", "primary_command": "dlg-intentionally-missing-agent"}, "user")
	must(t, err)
	out, meta, err := (&server{l}).runPrimary(nil, "analyse", "agreement board", false, runOpts{cfg: &original})
	must(t, err)
	if !strings.Contains(out, "summary") || meta["agent"] != "original" {
		t.Fatal("active run changed configuration", meta)
	}
}

func TestExplicitAnswerPointRulings(t *testing.T) {
	l := newLedger(t)
	pid, err := l.createProblem("Saved answers and decisions", "", nil, "user")
	must(t, err)
	analysis := `{"summary":"Models agree","solution":{"title":"Local app"},"questions":[{"question":"Persist tasks?","assumption":"In-memory only"}],"points":[{"key":"P1","topic":"Persistence","primary":"In-memory only","positions":{"reviewer":"Agrees"},"status":"decided"},{"key":"P2","topic":"Framework","status":"agreed"}]}`
	aid, err := l.addContribution(pid, "primary", "analysis", analysis)
	must(t, err)
	c := client{t, newServer(l)}
	answers := fmt.Sprintf("/api/problems/%d/answers", pid)
	links := fmt.Sprintf("/api/problems/%d/answer-rulings", pid)
	// Saving an assumption alone is still context, not permission to rule on arbitrary points.
	c.post(answers, fmt.Sprintf(`{"analysis_id":%d,"index":0,"use_assumption":true}`, aid))
	d, err := l.problemDetail(pid)
	must(t, err)
	answerID := ofKind(d, "context")[0].num("id")
	_, a, _ := latestAnalysis(d)
	if settled(a.Points[0], rulings(d)) {
		t.Fatal("unlinked answer silently settled a point")
	}
	// An older saved answer can be explicitly reused after auto-rounds without retyping it.
	latest, err := l.addContribution(pid, "primary", "analysis", analysis)
	must(t, err)
	payload := fmt.Sprintf(`{"analysis_id":%d,"answer_id":%d,"point_keys":["P1","P1"]}`, latest, answerID)
	c.post(links, payload)
	c.post(links, payload)
	d, err = l.problemDetail(pid)
	must(t, err)
	_, a, _ = latestAnalysis(d)
	if !settled(a.Points[0], rulings(d)) || len(ofKind(d, "context")) != 2 {
		t.Fatal("ruling missing or duplicated")
	}
	lines := strings.Join(decisionLines(d, a), "\n")
	if !strings.Contains(lines, "[P1] user ruling:") || !strings.Contains(lines, "In-memory only") {
		t.Fatal(lines)
	}
	status, err := problemStatus(l, pid)
	must(t, err)
	if status.Settled != 2 {
		t.Fatalf("CLI/API still reports unresolved points: %+v", status)
	}
	// Invalid point, stale round, unrelated contribution and wrong problem must fail without writes.
	other, err := l.createProblem("Other", "", nil, "user")
	must(t, err)
	foreign, err := l.addContributionMeta(other, "user", "context", "Q: Other A: Yes", D{"analysis_id": aid, "question_index": 0, "answer": "Yes"})
	must(t, err)
	for _, bad := range []string{
		fmt.Sprintf(`{"analysis_id":%d,"answer_id":%d,"point_keys":["P9"]}`, latest, answerID),
		fmt.Sprintf(`{"analysis_id":%d,"answer_id":%d,"point_keys":["P1"]}`, aid, answerID),
		fmt.Sprintf(`{"analysis_id":%d,"answer_id":%d,"point_keys":["P1"]}`, latest, latest),
		fmt.Sprintf(`{"analysis_id":%d,"answer_id":%d,"point_keys":["P1"]}`, latest, foreign),
	} {
		if code, _ := c.call("POST", "http://127.0.0.1"+links, bad, "X-Ledger", "1"); code != 400 {
			t.Fatalf("accepted invalid link %s (%d)", bad, code)
		}
	}
	// Answer plus user-selected rulings are saved together and supersede previous rulings.
	c.post(answers, fmt.Sprintf(`{"analysis_id":%d,"index":0,"answer":"Use SQLite","point_keys":["P1"]}`, latest))
	d, err = l.problemDetail(pid)
	must(t, err)
	if !strings.Contains(rulings(d)["P1"], "Use SQLite") {
		t.Fatal(rulings(d))
	}
	before := len(d.Contributions)
	if code, _ := c.call("POST", "http://127.0.0.1"+answers, fmt.Sprintf(`{"analysis_id":%d,"index":0,"answer":"Must not be saved","point_keys":["missing"]}`, latest), "X-Ledger", "1"); code != 400 {
		t.Fatal(code)
	}
	d, err = l.problemDetail(pid)
	must(t, err)
	if len(d.Contributions) != before {
		t.Fatal("invalid link partially saved an answer")
	}
	// Changed answers cannot be accidentally replaced with an obsolete answer version.
	old := savedAnswer(d, latest, 0).ContributionID
	c.post(answers, fmt.Sprintf(`{"analysis_id":%d,"index":0,"answer":"Use JSON"}`, latest))
	if code, _ := c.call("POST", "http://127.0.0.1"+links, fmt.Sprintf(`{"analysis_id":%d,"answer_id":%d,"point_keys":["P1"]}`, latest, old), "X-Ledger", "1"); code != 400 {
		t.Fatal("accepted superseded answer", code)
	}
}

func TestBundledMarkdownAssets(t *testing.T) {
	handler := newServer(newLedger(t))
	for _, asset := range []struct{ path, mime, contains string }{
		{"/vendor/markdown-it.min.js", "text/javascript", "markdownit"},
		{"/vendor/markdown-it.LICENSE.txt", "text/plain", "Permission is hereby granted"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "http://127.0.0.1"+asset.path, nil))
		if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), asset.mime) || !strings.Contains(response.Body.String(), asset.contains) {
			t.Fatalf("bundled asset unavailable: %s (%d, %s)", asset.path, response.Code, response.Header().Get("Content-Type"))
		}
	}
}
