// HTTP app: a JSON API plus the single-page UI. The UI is the only way to view or change the ledger.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

//go:embed static
var static embed.FS

type server struct{ l *Ledger }

type (
	problemIn struct {
		Title    string `json:"title"`
		Context  string `json:"context"`
		ParentID *int64 `json:"parent_id"`
	}
	problemPatch struct {
		Title   *string `json:"title"`
		Context *string `json:"context"`
	}
	alternativeIn struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	alternativePatch struct {
		Title       *string `json:"title"`
		Description *string `json:"description"`
	}
	noteIn struct {
		Body string `json:"body"`
	}
	evidenceIn struct {
		Quote         string `json:"quote"`
		Source        string `json:"source"`
		AlternativeID *int64 `json:"alternative_id"`
	}
	handoffIn struct {
		To string `json:"to"`
	}
	reviewIn struct {
		Author string `json:"author"`
		Body   string `json:"body"`
		Meta   D      `json:"meta"` // runner details (`dlg run`); optional
	}
	resolveIn struct {
		Resolution  string `json:"resolution"`
		Note        string `json:"note"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	selectIn struct {
		AlternativeID *int64 `json:"alternative_id"`
		Rationale     string `json:"rationale"`
	}
	approveIn struct {
		Note string `json:"note"`
	}
	implementIn struct {
		ProblemIDs []int64 `json:"problem_ids"`
		Model      string  `json:"model"` // preset key only
	}
	runIn struct {
		Model string `json:"model"` // preset key only
	}
	cycleIn struct {
		Rounds int    `json:"rounds"`
		Model  string `json:"model"` // preset key only, for the primary
	}
	cancelIn struct {
		Revert bool `json:"revert"`
	}
)

var localHosts = map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true}

// localOnly blocks other sites (CSRF) and DNS rebinding, because the ledger runs shell
// commands: the Host must be local, and writes need a custom header, which forces a CORS
// preflight that this server never approves.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = strings.Trim(r.Host, "[]")
		}
		if !localHosts[host] {
			writeJSON(w, http.StatusForbidden, D{"detail": "forbidden host"})
			return
		}
		// GET /api/models and /api/runners/{agent}/models run list commands, so they are guarded like a write.
		if (r.Method != http.MethodGet || strings.HasSuffix(r.URL.Path, "/models")) && r.Header.Get("X-Ledger") != "1" {
			writeJSON(w, http.StatusForbidden, D{"detail": "missing X-Ledger header"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	var agentErr AgentError
	switch {
	case errors.As(err, new(LedgerError)):
		writeJSON(w, http.StatusBadRequest, D{"detail": err.Error()})
	case errors.As(err, &agentErr):
		writeJSON(w, http.StatusBadGateway, D{"detail": "agent: " + err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, D{"detail": err.Error()})
	}
}

// api adapts a handler that returns a JSON value or an error.
func api(h func(r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := h(r)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil {
		return 0, ledgerErr("invalid %s", name)
	}
	return id, nil
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return ledgerErr("invalid request body: %v", err)
	}
	return nil
}

// withID decodes the optional body, then calls fn with the numeric path id.
func withID[B any](name string, fn func(id int64, body B) (any, error)) http.HandlerFunc {
	return api(func(r *http.Request) (any, error) {
		id, err := pathID(r, name)
		if err != nil {
			return nil, err
		}
		var body B
		if r.ContentLength != 0 {
			if err := decode(r, &body); err != nil {
				return nil, err
			}
		}
		return fn(id, body)
	})
}

func idOf(id int64, err error) (any, error) { return D{"id": id}, err }

func okOf(err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return D{"ok": true}, nil
}

func newServer(l *Ledger) http.Handler {
	s := &server{l}
	mux := http.NewServeMux()
	index, _ := static.ReadFile("static/index.html")
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})

	mux.HandleFunc("GET /api/state", api(func(r *http.Request) (any, error) {
		settings, err := l.settings()
		if err != nil {
			return nil, err
		}
		problems, err := l.listProblems()
		if err != nil {
			return nil, err
		}
		// The sidebar marks problems where the next move is the user's; only live ones are checked.
		for _, p := range problems {
			if s := p.str("status"); s != "open" && s != "selected" {
				continue
			}
			st, err := problemStatus(l, p.num("id"))
			if err != nil {
				return nil, err
			}
			p["needs_you"] = len(st.ToSend) > 0 || st.NewReplies > 0 || st.Settled < st.Points
		}
		// The Settings tab offers paste lines only for these names.
		agents := []D{}
		for _, a := range knownAgents {
			agents = append(agents, D{"name": a.name, "primary": a.primary, "implement": a.implement})
		}
		return D{"settings": settings, "problems": problems, "ledger": l.path, "agents": agents}, nil
	}))
	mux.HandleFunc("PUT /api/settings", api(func(r *http.Request) (any, error) {
		values := D{}
		if err := decode(r, &values); err != nil {
			return nil, err
		}
		// These keys make the ledger run shell commands (or choose where); a web page must never set them.
		for _, k := range cliOnlySettings {
			if _, ok := values[k]; ok {
				return nil, ledgerErr("%s can only be changed with the dlg command line", k)
			}
		}
		return l.updateSettings(values, "user")
	}))

	mux.HandleFunc("POST /api/problems", api(func(r *http.Request) (any, error) {
		var b problemIn
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		return idOf(l.createProblem(b.Title, b.Context, b.ParentID, "user"))
	}))
	mux.HandleFunc("GET /api/problems/{pid}", withID("pid", func(id int64, _ struct{}) (any, error) {
		return s.detailWithHandoffs(id)
	}))
	mux.HandleFunc("PATCH /api/problems/{pid}", withID("pid", func(id int64, b problemPatch) (any, error) {
		return okOf(l.updateProblem(id, b.Title, b.Context))
	}))
	mux.HandleFunc("DELETE /api/problems/{pid}", withID("pid", func(id int64, _ struct{}) (any, error) {
		return okOf(l.deleteProblem(id))
	}))

	mux.HandleFunc("POST /api/problems/{pid}/alternatives", withID("pid", func(id int64, b alternativeIn) (any, error) {
		return idOf(l.addAlternative(id, b.Title, b.Description, "user"))
	}))
	mux.HandleFunc("PATCH /api/alternatives/{aid}", withID("aid", func(id int64, b alternativePatch) (any, error) {
		return okOf(l.updateAlternative(id, b.Title, b.Description))
	}))
	mux.HandleFunc("DELETE /api/alternatives/{aid}", withID("aid", func(id int64, _ struct{}) (any, error) {
		return okOf(l.deleteAlternative(id))
	}))

	mux.HandleFunc("POST /api/problems/{pid}/notes", withID("pid", func(id int64, b noteIn) (any, error) {
		return idOf(addNote(l, id, b.Body))
	}))
	mux.HandleFunc("POST /api/problems/{pid}/evidence", withID("pid", func(id int64, b evidenceIn) (any, error) {
		if b.Quote == "" {
			return nil, ledgerErr("a quote is required")
		}
		return idOf(l.addEvidence(id, b.Quote, b.Source, "user", b.AlternativeID, nil))
	}))

	mux.HandleFunc("POST /api/problems/{pid}/analyse", withID("pid", func(pid int64, b runIn) (any, error) {
		cfg, err := l.settings()
		if err != nil {
			return nil, err
		}
		if _, err := l.require("problems", pid); err != nil {
			return nil, err
		}
		if _, _, err := resolveModel(cfg, b.Model, false); err != nil {
			return nil, err
		}
		j, err := jobs.start("analyse", []int64{pid}, b.Model, cfg.Workdir, func(ctx context.Context, out io.Writer) (any, error) {
			return s.analyseRun(pid, runOpts{ctx, out, b.Model})
		})
		if err != nil {
			return nil, err
		}
		return D{"job_id": j.id}, nil
	}))
	mux.HandleFunc("POST /api/problems/{pid}/cycle", withID("pid", func(pid int64, b cycleIn) (any, error) {
		if b.Rounds < 1 || b.Rounds > maxCycleRounds {
			return nil, ledgerErr("rounds must be 1 to %d", maxCycleRounds)
		}
		cfg, err := l.settings()
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(cfg.SecondaryNames, func(n string) bool { return runnable(cfg, n) }) {
			return nil, ledgerErr("no reviewer has an approved runner; set and approve one in a terminal: dlg config set-runner <agent> (or approve a stored one with dlg run)")
		}
		if _, err := l.require("problems", pid); err != nil {
			return nil, err
		}
		if _, _, err := resolveModel(cfg, b.Model, false); err != nil {
			return nil, err
		}
		j, err := jobs.start("cycle", []int64{pid}, b.Model, cfg.Workdir, func(ctx context.Context, out io.Writer) (any, error) {
			return s.cycleRun(pid, b.Rounds, runOpts{ctx, out, b.Model})
		})
		if err != nil {
			return nil, err
		}
		return D{"job_id": j.id}, nil
	}))
	// Runs a reviewer's CLI. The command and its approval come only from the terminal
	// (`dlg config set-runner`, `dlg run`): a click can start an approved command, never choose or approve one.
	mux.HandleFunc("POST /api/problems/{pid}/runs/{agent}", api(func(r *http.Request) (any, error) {
		pid, err := pathID(r, "pid")
		if err != nil {
			return nil, err
		}
		agent := r.PathValue("agent")
		cfg, err := l.settings()
		if err != nil {
			return nil, err
		}
		if _, err := l.require("problems", pid); err != nil {
			return nil, err
		}
		command := cfg.SecondaryCommands[agent]
		if command == "" {
			return nil, ledgerErr("no runner for %s; set and approve one in a terminal: dlg config set-runner %s [command]", agent, agent)
		}
		if !slices.Contains(cfg.RunnerConfirmed, command) {
			return nil, ledgerErr("%q is not approved yet; approve it once in a terminal: dlg config set-runner %s <command>, or dlg run %d %s", command, agent, pid, agent)
		}
		var b struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil && !errors.Is(err, io.EOF) {
			return nil, ledgerErr("invalid body: %v", err)
		}
		list, _ := listRunnerModels(cfg, agent, false)
		_, model, err := reviewerCommand(cfg, agent, b.Model, list)
		if err != nil {
			return nil, err
		}
		j, err := jobs.start("review", []int64{pid}, model, cfg.Workdir, func(ctx context.Context, out io.Writer) (any, error) {
			rv, meta, err := runSecondary(ctx, l, localCore{l}, cfg, pid, agent, b.Model, runTimeout, out)
			if err != nil {
				return nil, err
			}
			return D{"review_id": rv.ID, "warnings": rv.Warnings, "meta": meta}, nil
		})
		if err != nil {
			return nil, err
		}
		j.mu.Lock()
		j.agent = agent
		j.mu.Unlock()
		return D{"job_id": j.id}, nil
	}))
	// A reviewer's models, from its runner CLI (runs a command, so guarded like a write).
	mux.HandleFunc("GET /api/runners/{agent}/models", api(func(r *http.Request) (any, error) {
		cfg, err := l.settings()
		if err != nil {
			return nil, err
		}
		agent := r.PathValue("agent")
		list, errMsg := listRunnerModels(cfg, agent, r.URL.Query().Get("refresh") == "1")
		if list == nil {
			list = []modelView{}
		}
		fixed := ""
		if m := runnerModel(cfg.SecondaryCommands[agent]); m != "default" {
			fixed = m
		}
		return D{"models": list, "error": errMsg, "fixed": fixed}, nil
	}))
	mux.HandleFunc("GET /api/models", api(func(r *http.Request) (any, error) {
		cfg, err := l.settings()
		if err != nil {
			return nil, err
		}
		list, errMsg := availableModels(cfg, r.URL.Query().Get("refresh") == "1")
		views := []modelView{}
		for _, m := range list {
			views = append(views, modelView{m.Key, m.Label, m.contextLabel(), m.Model, m.Context, m.Effort})
		}
		return D{"models": views, "error": errMsg}, nil
	}))
	mux.HandleFunc("GET /api/jobs/{jid}", api(func(r *http.Request) (any, error) {
		j := jobs.get(r.PathValue("jid"))
		if j == nil {
			return nil, ledgerErr("unknown job (the server may have restarted)")
		}
		return j.view(), nil
	}))
	mux.HandleFunc("POST /api/jobs/{jid}/cancel", api(func(r *http.Request) (any, error) {
		var b cancelIn
		if r.ContentLength != 0 {
			if err := decode(r, &b); err != nil {
				return nil, err
			}
		}
		j, err := jobs.stop(r.PathValue("jid"), b.Revert)
		if err != nil {
			return nil, err
		}
		var pid any
		if len(j.problems) > 0 {
			pid = j.problems[0]
		}
		if err := l.record("user", "cancel_job", pid, D{"job": j.id, "kind": j.kind, "revert": b.Revert}); err != nil {
			return nil, err
		}
		return j.view(), nil
	}))
	mux.HandleFunc("GET /api/problems/{pid}/job", withID("pid", func(pid int64, _ struct{}) (any, error) {
		if j := jobs.latestFor(pid); j != nil {
			return D{"job": j.view()}, nil
		}
		return D{"job": nil}, nil
	}))
	mux.HandleFunc("POST /api/problems/{pid}/handoffs", withID("pid", func(pid int64, b handoffIn) (any, error) {
		return s.recordHandoff(pid, b.To)
	}))
	mux.HandleFunc("POST /api/problems/{pid}/finalise", withID("pid", func(pid int64, b runIn) (any, error) {
		cfg, err := l.settings()
		if err != nil {
			return nil, err
		}
		if _, err := l.require("problems", pid); err != nil {
			return nil, err
		}
		if _, _, err := resolveModel(cfg, b.Model, false); err != nil {
			return nil, err
		}
		j, err := jobs.start("finalise", []int64{pid}, b.Model, cfg.Workdir, func(ctx context.Context, out io.Writer) (any, error) {
			return s.finaliseRun(pid, runOpts{ctx, out, b.Model})
		})
		if err != nil {
			return nil, err
		}
		return D{"job_id": j.id}, nil
	}))
	mux.HandleFunc("POST /api/problems/{pid}/reviews", withID("pid", func(pid int64, b reviewIn) (any, error) {
		// Stored verbatim before any agent sees it.
		return addReview(l, pid, b.Author, b.Body, b.Meta)
	}))
	mux.HandleFunc("POST /api/reviews/{rid}/parse", withID("rid", func(id int64, _ struct{}) (any, error) {
		return parseReview(l, id)
	}))
	mux.HandleFunc("GET /api/problems/{pid}/status", withID("pid", func(id int64, _ struct{}) (any, error) {
		return problemStatus(l, id)
	}))
	mux.HandleFunc("GET /api/problems/{pid}/handoffs/{to}", api(func(r *http.Request) (any, error) {
		id, err := pathID(r, "pid")
		if err != nil {
			return nil, err
		}
		return peekHandoff(l, id, r.PathValue("to"))
	}))

	mux.HandleFunc("POST /api/disagreements/{did}/resolve", withID("did", func(id int64, b resolveIn) (any, error) {
		return okOf(l.resolveDisagreement(id, b.Resolution, b.Note, b.Title, b.Description))
	}))
	mux.HandleFunc("POST /api/problems/{pid}/select", withID("pid", func(id int64, b selectIn) (any, error) {
		return idOf(l.selectAlternative(id, b.AlternativeID, b.Rationale))
	}))
	mux.HandleFunc("POST /api/problems/{pid}/approve", withID("pid", func(id int64, b approveIn) (any, error) {
		return idOf(l.approve(id, b.Note))
	}))
	mux.HandleFunc("POST /api/implement", api(func(r *http.Request) (any, error) {
		var b implementIn
		if err := decode(r, &b); err != nil {
			return nil, err
		}
		payloads, err := s.implementPayloads(b.ProblemIDs)
		if err != nil {
			return nil, err
		}
		cfg, err := l.settings()
		if err != nil {
			return nil, err
		}
		if _, _, err := resolveModel(cfg, b.Model, true); err != nil {
			return nil, err
		}
		j, err := jobs.start("implement", b.ProblemIDs, b.Model, cfg.Workdir, func(ctx context.Context, out io.Writer) (any, error) {
			return s.implementRun(b.ProblemIDs, payloads, runOpts{ctx, out, b.Model})
		})
		if err != nil {
			return nil, err
		}
		return D{"job_id": j.id}, nil
	}))

	mux.HandleFunc("GET /api/export", func(w http.ResponseWriter, r *http.Request) {
		data, err := l.export()
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Disposition", "attachment; filename=ledger.json")
		writeJSON(w, http.StatusOK, data)
	})
	mux.HandleFunc("POST /api/import", api(func(r *http.Request) (any, error) {
		var raw json.RawMessage
		if err := decode(r, &raw); err != nil {
			return nil, err
		}
		data, err := parseExport(raw)
		if err != nil {
			return nil, err
		}
		return okOf(l.importData(data))
	}))
	return localOnly(mux)
}

// runOpts is how one run differs from the default: cancellable, observable, on a preset.
// The zero value runs blocking with the stage's default model.
type runOpts struct {
	ctx   context.Context
	out   io.Writer
	model string // preset key; "" = the stage default
}

// runPrimary runs the primary agent's command and audits the run. meta is what to store with
// the output (usage, preset); with a problem, the usage is also audited, failed runs included.
func (s *server) runPrimary(problemID any, op, prompt string, implement bool, o runOpts) (string, D, error) {
	cfg, err := s.l.settings()
	if err != nil {
		return "", nil, err
	}
	command, preset, err := resolveModel(cfg, o.model, implement)
	if err != nil {
		return "", nil, err
	}
	detail := D{"agent": cfg.PrimaryName, "command": command}
	if preset != nil {
		detail["model"] = preset.Key
	}
	if err := s.l.record("user", "run_"+op, problemID, detail); err != nil {
		return "", nil, err
	}
	if o.ctx == nil {
		o.ctx = context.Background()
	}
	out, usage, err := runAgentCtx(o.ctx, command, prompt, cfg.Workdir, o.out)
	meta := usageMeta(usage, op, presetText(preset), err != nil)
	addJobUsage(o.ctx, usage, err != nil)
	if usage != nil && problemID != nil {
		if recErr := s.l.record(cfg.PrimaryName, "usage", problemID, meta); recErr != nil {
			return "", nil, recErr
		}
	}
	if err != nil {
		if recErr := s.l.record(cfg.PrimaryName, op+"_failed", problemID, D{"error": err.Error()}); recErr != nil {
			return "", nil, recErr
		}
	}
	return out, meta, err
}

// presetText names the user's model choice for a run, e.g. "opus-1m (opus, 1m, high)".
func presetText(p *ModelPreset) string {
	if p == nil {
		return "default"
	}
	fields := slices.DeleteFunc([]string{p.Model, p.Context, p.Effort}, func(s string) bool { return s == "" })
	if len(fields) == 0 {
		return p.Key
	}
	return fmt.Sprintf("%s (%s)", p.Key, strings.Join(fields, ", "))
}

// usageMeta is the run's usage as metadata and as the detail of a "usage" audit event,
// the source of the cost totals. model is what the user selected for the run.
func usageMeta(u *Usage, op, model string, failed bool) D {
	if u == nil {
		return nil
	}
	m := D{"usage": u, "op": op}
	if model != "" {
		m["model"] = model
	}
	if failed {
		m["failed"] = true
	}
	return m
}

// -- consensus rounds: all state is derived from contributions -------------

// Handoff is what the user copies to one secondary model next.
type Handoff struct {
	To         string `json:"to"`
	State      string `json:"state"` // unsent | changed | sent | replied
	Mode       string `json:"mode"`  // full | delta
	AnalysisID int64  `json:"analysis_id"`
	Text       string `json:"text"`
	HasCode    bool   `json:"has_code"`
}

// handoffRecord is the body of a "handoff" contribution.
type handoffRecord struct {
	To         string `json:"to"`
	AnalysisID int64  `json:"analysis_id"`
	Mode       string `json:"mode"`
	Text       string `json:"text"`
}

func ofKind(d *Detail, kind string) []Row {
	rows := []Row{}
	for _, c := range d.Contributions {
		if c.str("kind") == kind {
			rows = append(rows, c)
		}
	}
	return rows
}

// analysisOf parses an "analysis" contribution, which the ledger itself wrote.
func analysisOf(c Row) *AnalysisOut {
	var a AnalysisOut
	if c == nil || json.Unmarshal([]byte(c.str("body")), &a) != nil {
		return nil
	}
	return &a
}

// latestAnalysis returns the newest analysis and its round number (0 if none).
func latestAnalysis(d *Detail) (Row, *AnalysisOut, int) {
	analyses := ofKind(d, "analysis")
	if len(analyses) == 0 {
		return nil, nil, 0
	}
	last := analyses[len(analyses)-1]
	return last, analysisOf(last), len(analyses)
}

var rulingRe = regexp.MustCompile(`(?s)^Ruling on \[([^\]]+)\]\s*(.*)$`)

// rulings maps a board point key to the user's latest "Ruling on [Pn]" note text.
func rulings(d *Detail) map[string]string {
	out := map[string]string{}
	for _, c := range ofKind(d, "context") {
		if m := rulingRe.FindStringSubmatch(c.str("body")); m != nil {
			out[m[1]] = strings.TrimSpace(m[2])
		}
	}
	return out
}

// decisionLines is the settled-points list for the implementer: the user's rulings, plus
// points the agent reports every model agreed on (labelled as the agent's claim). "decided"
// is only the agent's word, so without a ruling note it counts as unsettled and is left out.
func decisionLines(d *Detail, a *AnalysisOut) []string {
	lines := []string{}
	if a == nil {
		return lines
	}
	ruled := rulings(d)
	for _, p := range a.Points {
		switch r, ok := ruled[p.Key]; {
		case ok:
			lines = append(lines, fmt.Sprintf("[%s] user ruling: %s", p.Key, r))
		case p.Status == "agreed":
			lines = append(lines, fmt.Sprintf("[%s] %s: the models agreed (%s); no user ruling", p.Key, p.Topic, p.Primary))
		}
	}
	return lines
}

// settled reports whether a board point needs no more input: the user ruled on it, or the
// agent reports agreement. A "decided" status without a ruling note is not trusted.
func settled(p PointOut, ruled map[string]string) bool {
	_, byUser := ruled[p.Key]
	return byUser || p.Status == "agreed"
}

// notesAfter returns user notes newer than contribution id.
func notesAfter(d *Detail, id int64) []string {
	notes := []string{}
	for _, c := range ofKind(d, "context") {
		if c.num("id") > id {
			notes = append(notes, c.str("body"))
		}
	}
	return notes
}

func handoffs(d *Detail, primary string, names, withCode []string) []Handoff {
	latest, cur, round := latestAnalysis(d)
	out := []Handoff{}
	if cur == nil {
		return out
	}
	byID := map[int64]Row{}
	for _, c := range d.Contributions {
		byID[c.num("id")] = c
	}
	for _, name := range names {
		h := Handoff{To: name, AnalysisID: latest.num("id"), HasCode: slices.Contains(withCode, name)}
		var last Row
		var rec handoffRecord
		for _, c := range ofKind(d, "handoff") {
			var r handoffRecord
			if json.Unmarshal([]byte(c.str("body")), &r) == nil && r.To == name {
				last, rec = c, r
			}
		}
		prev := analysisOf(byID[rec.AnalysisID])
		switch {
		case last != nil && rec.AnalysisID == h.AnalysisID:
			h.State, h.Mode, h.Text = "sent", rec.Mode, rec.Text
			for _, r := range ofKind(d, "review") {
				if r.str("author") == name && r.num("id") > last.num("id") {
					h.State = "replied"
				}
			}
		case last != nil && prev != nil:
			changes := []string{}
			for _, c := range ofKind(d, "analysis") {
				if a := analysisOf(c); a != nil && c.num("id") > rec.AnalysisID {
					changes = append(changes, a.Changes...)
				}
			}
			h.State, h.Mode = "changed", "delta"
			h.Text = deltaHandoff(d, prev, cur, primary, name, h.HasCode, round, notesAfter(d, last.num("id")), changes)
		default:
			h.State, h.Mode = "unsent", "full"
			h.Text = fullHandoff(d, cur, primary, name, h.HasCode, round, notesAfter(d, 0))
		}
		out = append(out, h)
	}
	return out
}

// detailWithHandoffs is the problem view the UI renders.
func (s *server) detailWithHandoffs(pid int64) (*Detail, error) {
	d, err := s.l.problemDetail(pid)
	if err != nil {
		return nil, err
	}
	cfg, err := s.l.settings()
	if err != nil {
		return nil, err
	}
	d.Handoffs = handoffs(d, cfg.PrimaryName, cfg.SecondaryNames, cfg.SecondaryWithCode)
	d.Cost = costOf(d)
	return d, nil
}

// recordHandoff stores the text the user just copied, so the next copy is a delta.
func (s *server) recordHandoff(pid int64, to string) (any, error) {
	d, err := s.detailWithHandoffs(pid)
	if err != nil {
		return nil, err
	}
	for _, h := range d.Handoffs {
		if h.To != to {
			continue
		}
		if h.State == "sent" || h.State == "replied" {
			return D{"ok": true}, nil // copied again; nothing new was sent
		}
		body, _ := json.Marshal(handoffRecord{To: to, AnalysisID: h.AnalysisID, Mode: h.Mode, Text: h.Text})
		return idOf(s.l.addContribution(pid, "user", "handoff", string(body)))
	}
	return nil, ledgerErr("%q is not a configured secondary agent, or there is no analysis yet", to)
}

// analyse runs one round: the first analysis, a re-analysis after the user's answers,
// or processing the replies pasted since the last round.
func (s *server) analyse(pid int64) (any, error) { return s.analyseRun(pid, runOpts{}) }

func (s *server) analyseRun(pid int64, o runOpts) (any, error) {
	cfg, err := s.l.settings()
	if err != nil {
		return nil, err
	}
	detail, err := s.l.problemDetail(pid)
	if err != nil {
		return nil, err
	}
	latest, prev, round := latestAnalysis(detail)
	replies := []Row{}
	for _, r := range ofKind(detail, "review") {
		if latest == nil || r.num("id") > latest.num("id") {
			replies = append(replies, r)
		}
	}
	output, meta, err := s.runPrimary(pid, "analyse", analysePrompt(detail, prev, replies), false, o)
	if err != nil {
		return nil, err
	}
	if _, err := s.l.addContributionMeta(pid, cfg.PrimaryName, "proposal", output, meta); err != nil {
		return nil, err
	}
	analysis, err := parseAgent[AnalysisOut](output)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(analysis)
	if _, err := s.l.addContributionMeta(pid, cfg.PrimaryName, "analysis", string(body), meta); err != nil {
		return nil, err
	}
	return D{"round": round + 1, "replies": len(replies)}, nil
}

// finaliseRun has the primary write the consensus proposal and selects it; approval
// and implementation then work as for any alternative. An approved selection is left
// alone: the proposal is only added, so the approval does not go stale behind the user's back.
func (s *server) finaliseRun(pid int64, o runOpts) (any, error) {
	cfg, err := s.l.settings()
	if err != nil {
		return nil, err
	}
	detail, err := s.l.problemDetail(pid)
	if err != nil {
		return nil, err
	}
	_, latest, _ := latestAnalysis(detail)
	if latest == nil {
		return nil, ledgerErr("analyse the problem first")
	}
	output, meta, err := s.runPrimary(pid, "finalise", finalPrompt(detail, latest), false, o)
	if err != nil {
		return nil, err
	}
	if _, err := s.l.addContributionMeta(pid, cfg.PrimaryName, "final", output, meta); err != nil {
		return nil, err
	}
	final, err := parseAgent[FinalOut](output)
	if err != nil {
		return nil, err
	}
	aid, err := s.l.addAlternative(pid, final.Title, final.Description, "consensus")
	if err != nil {
		return nil, err
	}
	// Re-read: the status may have changed while the agent ran.
	if now, err := s.l.problemDetail(pid); err != nil {
		return nil, err
	} else if now.Status == "approved" || now.Status == "sent" {
		return D{"alternative_id": aid, "selected": false}, nil
	}
	if _, err := s.l.selectAlternative(pid, &aid, orDefault(final.Rationale, "consensus of the review rounds")); err != nil {
		return nil, err
	}
	return D{"alternative_id": aid, "selected": true}, nil
}

// implementPayloads collects what to implement, including the user's decision per board point.
func (s *server) implementPayloads(problemIDs []int64) ([]*Payload, error) {
	if len(problemIDs) == 0 {
		return nil, ledgerErr("no problems to implement")
	}
	var payloads []*Payload
	for _, pid := range problemIDs {
		p, err := s.l.implementationPayload(pid)
		if err != nil {
			return nil, err
		}
		d, err := s.l.problemDetail(pid)
		if err != nil {
			return nil, err
		}
		_, a, _ := latestAnalysis(d)
		p.Decisions = decisionLines(d, a)
		payloads = append(payloads, p)
	}
	return payloads, nil
}

func (s *server) implement(problemIDs []int64) (any, error) {
	payloads, err := s.implementPayloads(problemIDs)
	if err != nil {
		return nil, err
	}
	return s.implementRun(problemIDs, payloads, runOpts{})
}

// implementRun markSent only after the agent succeeded; a failed or cancelled run leaves the approval unsent.
func (s *server) implementRun(problemIDs []int64, payloads []*Payload, o runOpts) (any, error) {
	output, meta, err := s.runPrimary(nil, "implement", implementPrompt(payloads), true, o)
	cfg, cerr := s.l.settings()
	if cerr != nil {
		return nil, cerr
	}
	// One run serves every problem in it: each gets an equal share of its usage.
	if meta != nil {
		meta["shared"] = len(problemIDs)
		for _, pid := range problemIDs {
			if err := s.l.record(cfg.PrimaryName, "usage", pid, meta); err != nil {
				return nil, err
			}
		}
	}
	if err != nil {
		return nil, err
	}
	for _, pid := range problemIDs {
		if err := s.l.markSent(pid, output, cfg.PrimaryName, meta); err != nil {
			return nil, err
		}
	}
	return D{"output": output}, nil
}
