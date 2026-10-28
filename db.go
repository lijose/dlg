// SQLite storage. Every write records an audit event in the same transaction.
//
// Rules enforced here, not in the UI:
//   - Only the user resolves a disagreement (resolveDisagreement always records actor "user").
//   - Approval is a separate record bound to one selection; re-selecting makes it stale.
//   - Implementation payloads only include approved problems, leave out open/deferred
//     disagreements, and list rejected alternatives as out of scope.
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	ledgerDir    = ".decision-ledger"
	dbName       = "ledger.db"
	exportFormat = 1
)

const ledgerSchema = `
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS problems (
    id INTEGER PRIMARY KEY,
    parent_id INTEGER REFERENCES problems(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    context TEXT NOT NULL DEFAULT '',
    author TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS alternatives (
    id INTEGER PRIMARY KEY,
    problem_id INTEGER NOT NULL REFERENCES problems(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    author TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS contributions (
    id INTEGER PRIMARY KEY,
    problem_id INTEGER NOT NULL REFERENCES problems(id) ON DELETE CASCADE,
    author TEXT NOT NULL,
    kind TEXT NOT NULL,  -- context | proposal | analysis | handoff | review | final | implementation (brief, evaluation: legacy)
    body TEXT NOT NULL,
    created_at TEXT NOT NULL,
    meta TEXT  -- optional JSON: runner details (run_id, command, exit_code, duration_ms, truncated), parsed reply
);
CREATE TABLE IF NOT EXISTS evidence (
    id INTEGER PRIMARY KEY,
    problem_id INTEGER NOT NULL REFERENCES problems(id) ON DELETE CASCADE,
    alternative_id INTEGER REFERENCES alternatives(id) ON DELETE SET NULL,
    contribution_id INTEGER REFERENCES contributions(id) ON DELETE SET NULL,
    quote TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    author TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS disagreements (
    id INTEGER PRIMARY KEY,
    problem_id INTEGER NOT NULL REFERENCES problems(id) ON DELETE CASCADE,
    alternative_id INTEGER REFERENCES alternatives(id) ON DELETE SET NULL,
    contribution_id INTEGER REFERENCES contributions(id) ON DELETE SET NULL,
    raised_by TEXT NOT NULL,
    summary TEXT NOT NULL,
    quote TEXT NOT NULL DEFAULT '',
    recommendation TEXT,  -- primary agent's advice: accept | reject
    recommendation_reason TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'open',  -- open | resolved | deferred
    resolution TEXT,  -- accept | reject | own | defer
    resolution_note TEXT NOT NULL DEFAULT '',
    resolved_alternative_id INTEGER,
    created_at TEXT NOT NULL,
    resolved_at TEXT
);
CREATE TABLE IF NOT EXISTS selections (
    id INTEGER PRIMARY KEY,
    problem_id INTEGER NOT NULL REFERENCES problems(id) ON DELETE CASCADE,
    alternative_id INTEGER,  -- NULL means deferred
    rationale TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS approvals (
    id INTEGER PRIMARY KEY,
    problem_id INTEGER NOT NULL REFERENCES problems(id) ON DELETE CASCADE,
    selection_id INTEGER NOT NULL REFERENCES selections(id) ON DELETE CASCADE,
    note TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    sent_at TEXT
);
-- No foreign key: the audit trail outlives deleted problems.
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY,
    ts TEXT NOT NULL,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    problem_id INTEGER,
    detail TEXT NOT NULL DEFAULT '{}'
);
`

// Export order respects foreign keys. Settings are machine-local and never imported,
// so an imported file cannot change which command the ledger runs.
var dataTables = []string{"problems", "alternatives", "contributions", "evidence",
	"disagreements", "selections", "approvals", "events"}

type Settings struct {
	PrimaryName       string   `json:"primary_name"`
	PrimaryCommand    string   `json:"primary_command"`
	ImplementCommand  string   `json:"implement_command"`
	Workdir           string   `json:"workdir"`
	SecondaryNames    []string `json:"secondary_names"`
	SecondaryWithCode []string `json:"secondary_with_code"` // reviewers that can open the repository themselves (e.g. an IDE)
	LastProblemID     *int64   `json:"last_problem_id"`
	// Opt-in runner (`dlg run`): reviewer name -> command that reads the handoff on stdin.
	// CLI/config only: the HTTP API refuses to write these two keys.
	SecondaryCommands map[string]string `json:"secondary_commands"`
	RunnerConfirmed   []string          `json:"runner_confirmed"` // commands the user already approved
	// Model presets (`dlg config add-model`); the web only ever sends a preset key.
	Models                []ModelPreset `json:"models"`
	ModelListCommand      string        `json:"model_list_command"`
	DefaultAnalyseModel   string        `json:"default_analyse_model"`   // preset key; "" = the base command
	DefaultImplementModel string        `json:"default_implement_model"` // preset key; "" = the base command
	CycleRounds           int           `json:"cycle_rounds"`            // auto rounds per click in the UI; 0 = 1
	// Reviewer name -> default model id for its runner (picked in the UI from the CLI's list).
	ReviewModels map[string]string `json:"review_models"`
}

// cliOnlySettings are never writable over HTTP: they make the ledger execute shell commands.
var cliOnlySettings = []string{"secondary_commands", "runner_confirmed", "models", "model_list_command",
	"primary_command", "implement_command", "workdir"}

func defaultSettings() Settings {
	return Settings{PrimaryName: "codex", PrimaryCommand: "codex exec", ImplementCommand: "codex exec --full-auto",
		SecondaryNames: []string{"claude", "gemini"}}
}

// LedgerError is a request that breaks a ledger rule.
type LedgerError string

func (e LedgerError) Error() string { return string(e) }

func ledgerErr(format string, args ...any) error { return LedgerError(fmt.Sprintf(format, args...)) }

// Row is one database row; D is a free-form JSON object (audit detail, settings).
type (
	Row map[string]any
	D   = map[string]any
)

func (r Row) str(k string) string { s, _ := r[k].(string); return s }
func (r Row) num(k string) int64  { n, _ := r[k].(int64); return n }
func (r Row) isNil(k string) bool { return r[k] == nil }

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05+00:00") }

func dbPath(root string) string { return filepath.Join(root, ledgerDir, dbName) }

// opt turns a nil pointer into SQL NULL.
func opt[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func ptr[T any](v T) *T { return &v }

type Ledger struct {
	db   *sql.DB
	path string
}

func openLedger(path string) (*Ledger, error) {
	uri := "file:" + (&url.URL{Path: path}).EscapedPath() + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	// One connection serializes all access.
	db.SetMaxOpenConns(1)
	l := &Ledger{db, path}
	if _, err := db.Exec(ledgerSchema); err != nil {
		db.Close()
		return nil, err
	}
	if err := l.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return l, nil
}

// migrate applies additive changes to ledgers created by older versions.
func (l *Ledger) migrate() error {
	cols, err := l.all("PRAGMA table_info(contributions)")
	if err != nil {
		return err
	}
	for _, c := range cols {
		if c.str("name") == "meta" {
			return nil
		}
	}
	_, err = l.db.Exec("ALTER TABLE contributions ADD COLUMN meta TEXT")
	return err
}

func (l *Ledger) close() error { return l.db.Close() }

// -- helpers ---------------------------------------------------------------

func (l *Ledger) all(query string, args ...any) ([]Row, error) {
	rs, err := l.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		return nil, err
	}
	out := []Row{}
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := Row{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			row[c] = vals[i]
		}
		out = append(out, row)
	}
	return out, rs.Err()
}

func (l *Ledger) one(query string, args ...any) (Row, error) {
	rows, err := l.all(query, args...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

func (l *Ledger) require(table string, id int64) (Row, error) {
	row, err := l.one("SELECT * FROM "+table+" WHERE id = ?", id)
	if err == nil && row == nil {
		err = ledgerErr("%s %d not found", table, id)
	}
	return row, err
}

// tx runs fn in one transaction. The first failed statement poisons it: later calls
// are skipped and everything rolls back.
type tx struct {
	t   *sql.Tx
	err error
}

func (l *Ledger) tx(fn func(x *tx)) error {
	t, err := l.db.Begin()
	if err != nil {
		return err
	}
	x := &tx{t: t}
	fn(x)
	if x.err != nil {
		t.Rollback()
		return x.err
	}
	return t.Commit()
}

func (x *tx) exec(query string, args ...any) sql.Result {
	if x.err != nil {
		return nil
	}
	res, err := x.t.Exec(query, args...)
	x.err = err
	return res
}

func (x *tx) insert(table string, row Row) int64 {
	cols, marks, vals := []string{}, []string{}, []any{}
	for k, v := range row {
		cols, marks, vals = append(cols, k), append(marks, "?"), append(vals, v)
	}
	res := x.exec("INSERT INTO "+table+" ("+strings.Join(cols, ", ")+") VALUES ("+strings.Join(marks, ", ")+")", vals...)
	if res == nil {
		return 0
	}
	id, err := res.LastInsertId()
	x.err = err
	return id
}

func (x *tx) update(table string, id int64, changes D) {
	sets, vals := []string{}, []any{}
	for k, v := range changes {
		sets, vals = append(sets, k+" = ?"), append(vals, v)
	}
	x.exec("UPDATE "+table+" SET "+strings.Join(sets, ", ")+" WHERE id = ?", append(vals, id)...)
}

func (x *tx) event(actor, action string, problemID any, detail D) {
	if detail == nil {
		detail = D{}
	}
	b, err := json.Marshal(detail)
	if err != nil {
		x.err = err
		return
	}
	x.exec("INSERT INTO events (ts, actor, action, problem_id, detail) VALUES (?, ?, ?, ?, ?)",
		now(), actor, action, problemID, string(b))
}

func (l *Ledger) record(actor, action string, problemID any, detail D) error {
	return l.tx(func(x *tx) { x.event(actor, action, problemID, detail) })
}

// changes keeps only the fields the caller provided.
func changes(fields map[string]*string) D {
	ch := D{}
	for k, v := range fields {
		if v != nil {
			ch[k] = *v
		}
	}
	return ch
}

func merge(a, b D) D {
	out := D{}
	for _, m := range []D{a, b} {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// -- settings --------------------------------------------------------------

func (l *Ledger) settings() (Settings, error) {
	s := defaultSettings()
	if err := applyGlobalConfig(&s); err != nil {
		return s, err
	}
	rows, err := l.all("SELECT * FROM settings")
	if err != nil {
		return s, err
	}
	stored := map[string]json.RawMessage{}
	for _, r := range rows {
		stored[r.str("key")] = json.RawMessage(r.str("value"))
	}
	obj, _ := json.Marshal(stored)
	// Decoding over the defaults overwrites only the stored keys.
	return s, json.Unmarshal(obj, &s)
}

func (l *Ledger) updateSettings(values D, actor string) (Settings, error) {
	obj, err := json.Marshal(values)
	if err != nil {
		return Settings{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(obj))
	dec.DisallowUnknownFields()
	var s Settings
	if err := dec.Decode(&s); err != nil {
		return Settings{}, ledgerErr("invalid settings: %v", err)
	}
	for agent, m := range s.ReviewModels {
		if m != "" && m != noModel && !reviewerModelRe.MatchString(m) {
			return Settings{}, ledgerErr("invalid model %q for %s", m, agent)
		}
	}
	err = l.tx(func(x *tx) {
		for k, v := range values {
			b, _ := json.Marshal(v)
			x.exec("INSERT INTO settings (key, value) VALUES (?, ?) "+
				"ON CONFLICT(key) DO UPDATE SET value = excluded.value", k, string(b))
		}
		_, hasLast := values["last_problem_id"]
		if !hasLast || len(values) != 1 { // remembering the open problem is not worth an audit event
			x.event(actor, "update_settings", nil, values)
		}
	})
	if err != nil {
		return Settings{}, err
	}
	return l.settings()
}

// -- problems --------------------------------------------------------------

func (l *Ledger) createProblem(title, context string, parentID *int64, author string) (int64, error) {
	if strings.TrimSpace(title) == "" {
		return 0, ledgerErr("a title is required")
	}
	if parentID != nil {
		if _, err := l.require("problems", *parentID); err != nil {
			return 0, err
		}
	}
	var id int64
	err := l.tx(func(x *tx) {
		id = x.insert("problems", Row{"parent_id": opt(parentID), "title": title, "context": context,
			"author": author, "created_at": now()})
		x.event(author, "create_problem", id, D{"title": title, "parent_id": opt(parentID)})
	})
	return id, err
}

func (l *Ledger) updateProblem(id int64, title, context *string) error {
	if _, err := l.require("problems", id); err != nil {
		return err
	}
	ch := changes(map[string]*string{"title": title, "context": context})
	if len(ch) == 0 {
		return nil
	}
	return l.tx(func(x *tx) {
		x.update("problems", id, ch)
		x.event("user", "update_problem", id, ch)
	})
}

func (l *Ledger) deleteProblem(id int64) error {
	p, err := l.require("problems", id)
	if err != nil {
		return err
	}
	return l.tx(func(x *tx) {
		x.exec("DELETE FROM problems WHERE id = ?", id)
		x.event("user", "delete_problem", id, D{"title": p.str("title")})
	})
}

// latest returns each problem's newest row of table, keyed by problem id.
func (l *Ledger) latest(table string) (map[int64]Row, error) {
	rows, err := l.all("SELECT * FROM " + table + " WHERE id IN (SELECT MAX(id) FROM " + table + " GROUP BY problem_id)")
	out := map[int64]Row{}
	for _, r := range rows {
		out[r.num("problem_id")] = r
	}
	return out, err
}

func statusOf(selection, approval Row) string {
	switch {
	case selection == nil:
		return "open"
	case selection.isNil("alternative_id"):
		return "deferred"
	case approval != nil && approval.num("selection_id") == selection.num("id"):
		if approval.isNil("sent_at") {
			return "approved"
		}
		return "sent"
	}
	return "selected"
}

func (l *Ledger) listProblems() ([]Row, error) {
	sels, err := l.latest("selections")
	if err != nil {
		return nil, err
	}
	apprs, err := l.latest("approvals")
	if err != nil {
		return nil, err
	}
	counts, err := l.all("SELECT problem_id, COUNT(*) AS n FROM disagreements WHERE status = 'open' GROUP BY problem_id")
	if err != nil {
		return nil, err
	}
	open := map[int64]int64{}
	for _, c := range counts {
		open[c.num("problem_id")] = c.num("n")
	}
	problems, err := l.all("SELECT id, parent_id, title FROM problems ORDER BY id")
	for _, p := range problems {
		id := p.num("id")
		p["status"] = statusOf(sels[id], apprs[id])
		p["open_disagreements"] = open[id]
	}
	return problems, err
}

// problemState returns the status, the latest selection, and the approval if it is current.
func (l *Ledger) problemState(problemID int64) (string, Row, Row, error) {
	sel, err := l.one("SELECT * FROM selections WHERE problem_id = ? ORDER BY id DESC LIMIT 1", problemID)
	if err != nil {
		return "", nil, nil, err
	}
	appr, err := l.one("SELECT * FROM approvals WHERE problem_id = ? ORDER BY id DESC LIMIT 1", problemID)
	if err != nil {
		return "", nil, nil, err
	}
	status := statusOf(sel, appr)
	if status != "approved" && status != "sent" {
		appr = nil
	}
	return status, sel, appr, nil
}

func (l *Ledger) ancestors(problemID int64) ([]Row, error) {
	chain := []Row{}
	p, err := l.require("problems", problemID)
	for err == nil && !p.isNil("parent_id") {
		if p, err = l.require("problems", p.num("parent_id")); err == nil {
			chain = append([]Row{{"id": p.num("id"), "title": p.str("title"), "context": p.str("context")}}, chain...)
		}
	}
	return chain, err
}

type Detail struct {
	Problem         Row       `json:"problem"`
	Status          string    `json:"status"`
	Ancestors       []Row     `json:"ancestors"`
	Children        []Row     `json:"children"`
	Alternatives    []Row     `json:"alternatives"`
	GeneralEvidence []Row     `json:"general_evidence"`
	Contributions   []Row     `json:"contributions"`
	Disagreements   []Row     `json:"disagreements"`
	Selection       Row       `json:"selection"`
	Approval        Row       `json:"approval"`
	Events          []Row     `json:"events"`
	Handoffs        []Handoff `json:"handoffs,omitempty"` // filled by the server for the UI
	Cost            *CostOut  `json:"cost,omitempty"`     // likewise
}

func (l *Ledger) problemDetail(problemID int64) (*Detail, error) {
	problem, err := l.require("problems", problemID)
	if err != nil {
		return nil, err
	}
	d := &Detail{Problem: problem, GeneralEvidence: []Row{}, Children: []Row{}}
	if d.Status, d.Selection, d.Approval, err = l.problemState(problemID); err != nil {
		return nil, err
	}
	byProblem := func(table string) []Row {
		if err != nil {
			return nil
		}
		var rows []Row
		rows, err = l.all("SELECT * FROM "+table+" WHERE problem_id = ? ORDER BY id", problemID)
		return rows
	}
	evidence, alternatives := byProblem("evidence"), byProblem("alternatives")
	d.Contributions, d.Disagreements, d.Events = byProblem("contributions"), byProblem("disagreements"), byProblem("events")
	if err != nil {
		return nil, err
	}
	for _, alt := range alternatives {
		alt["evidence"] = []Row{}
		for _, e := range evidence {
			if !e.isNil("alternative_id") && e.num("alternative_id") == alt.num("id") {
				alt["evidence"] = append(alt["evidence"].([]Row), e)
			}
		}
	}
	for _, e := range evidence {
		if e.isNil("alternative_id") {
			d.GeneralEvidence = append(d.GeneralEvidence, e)
		}
	}
	d.Alternatives = alternatives
	if d.Ancestors, err = l.ancestors(problemID); err != nil {
		return nil, err
	}
	problems, err := l.listProblems()
	for _, p := range problems {
		if !p.isNil("parent_id") && p.num("parent_id") == problemID {
			d.Children = append(d.Children, p)
		}
	}
	return d, err
}

// -- alternatives, contributions, evidence ---------------------------------

func (l *Ledger) checkAlternative(problemID int64, alternativeID *int64) error {
	if alternativeID == nil {
		return nil
	}
	alt, err := l.require("alternatives", *alternativeID)
	if err == nil && alt.num("problem_id") != problemID {
		err = ledgerErr("alternative %d belongs to another problem", *alternativeID)
	}
	return err
}

func (l *Ledger) addAlternative(problemID int64, title, description, author string) (int64, error) {
	if strings.TrimSpace(title) == "" {
		return 0, ledgerErr("a title is required")
	}
	if _, err := l.require("problems", problemID); err != nil {
		return 0, err
	}
	var id int64
	err := l.tx(func(x *tx) {
		id = x.insert("alternatives", Row{"problem_id": problemID, "title": title, "description": description,
			"author": author, "created_at": now()})
		x.event(author, "add_alternative", problemID, D{"alternative_id": id, "title": title})
	})
	return id, err
}

func (l *Ledger) updateAlternative(id int64, title, description *string) error {
	alt, err := l.require("alternatives", id)
	if err != nil {
		return err
	}
	ch := changes(map[string]*string{"title": title, "description": description})
	if len(ch) == 0 {
		return nil
	}
	return l.tx(func(x *tx) {
		x.update("alternatives", id, ch)
		x.event("user", "update_alternative", alt.num("problem_id"), merge(D{"alternative_id": id}, ch))
	})
}

func (l *Ledger) deleteAlternative(id int64) error {
	alt, err := l.require("alternatives", id)
	if err != nil {
		return err
	}
	_, selection, _, err := l.problemState(alt.num("problem_id"))
	if err != nil {
		return err
	}
	if selection != nil && !selection.isNil("alternative_id") && selection.num("alternative_id") == id {
		return ledgerErr("cannot delete the selected alternative; select another or defer first")
	}
	return l.tx(func(x *tx) {
		x.exec("DELETE FROM alternatives WHERE id = ?", id)
		x.event("user", "delete_alternative", alt.num("problem_id"), D{"alternative_id": id, "title": alt.str("title")})
	})
}

func (l *Ledger) addContribution(problemID int64, author, kind, body string) (int64, error) {
	return l.addContributionMeta(problemID, author, kind, body, nil)
}

// addContributionMeta stores optional JSON metadata next to the raw body.
func (l *Ledger) addContributionMeta(problemID int64, author, kind, body string, meta D) (int64, error) {
	if _, err := l.require("problems", problemID); err != nil {
		return 0, err
	}
	var metaText any
	if len(meta) > 0 {
		b, err := json.Marshal(meta)
		if err != nil {
			return 0, err
		}
		metaText = string(b)
	}
	var id int64
	err := l.tx(func(x *tx) {
		id = x.insert("contributions", Row{"problem_id": problemID, "author": author, "kind": kind,
			"body": body, "created_at": now(), "meta": metaText})
		x.event(author, "add_"+kind, problemID, D{"contribution_id": id})
	})
	return id, err
}

// mergeMeta adds keys to a contribution's metadata.
func (l *Ledger) mergeMeta(id int64, patch D) error {
	c, err := l.require("contributions", id)
	if err != nil {
		return err
	}
	meta := D{}
	if s := c.str("meta"); s != "" {
		json.Unmarshal([]byte(s), &meta) // unreadable metadata is replaced
	}
	b, err := json.Marshal(merge(meta, patch))
	if err != nil {
		return err
	}
	return l.tx(func(x *tx) {
		x.update("contributions", id, D{"meta": string(b)})
		x.event("user", "update_contribution_meta", c.num("problem_id"), D{"contribution_id": id})
	})
}

func (l *Ledger) addEvidence(problemID int64, quote, source, author string, alternativeID, contributionID *int64) (int64, error) {
	if _, err := l.require("problems", problemID); err != nil {
		return 0, err
	}
	if err := l.checkAlternative(problemID, alternativeID); err != nil {
		return 0, err
	}
	var id int64
	err := l.tx(func(x *tx) {
		id = x.insert("evidence", Row{"problem_id": problemID, "alternative_id": opt(alternativeID),
			"contribution_id": opt(contributionID), "quote": quote, "source": source,
			"author": author, "created_at": now()})
		x.event(author, "add_evidence", problemID, D{"evidence_id": id, "alternative_id": opt(alternativeID)})
	})
	return id, err
}

// -- disagreements ---------------------------------------------------------

type DisagreementIn struct {
	ProblemID            int64
	RaisedBy, Summary    string
	Quote                string
	AlternativeID        *int64
	ContributionID       *int64
	Recommendation       string // "", accept or reject: the primary agent's advice
	RecommendationReason string
	RecordedBy           string
}

func (l *Ledger) addDisagreement(in DisagreementIn) (int64, error) {
	if _, err := l.require("problems", in.ProblemID); err != nil {
		return 0, err
	}
	if err := l.checkAlternative(in.ProblemID, in.AlternativeID); err != nil {
		return 0, err
	}
	if in.Recommendation != "" && in.Recommendation != "accept" && in.Recommendation != "reject" {
		return 0, ledgerErr("recommendation must be accept or reject")
	}
	var id int64
	err := l.tx(func(x *tx) {
		id = x.insert("disagreements", Row{
			"problem_id": in.ProblemID, "alternative_id": opt(in.AlternativeID), "contribution_id": opt(in.ContributionID),
			"raised_by": in.RaisedBy, "summary": in.Summary, "quote": in.Quote,
			"recommendation": nilIfEmpty(in.Recommendation), "recommendation_reason": in.RecommendationReason,
			"created_at": now()})
		x.event(in.RecordedBy, "add_disagreement", in.ProblemID, D{"disagreement_id": id,
			"raised_by": in.RaisedBy, "recommendation": nilIfEmpty(in.Recommendation)})
	})
	return id, err
}

// resolveDisagreement is the only way a disagreement leaves 'open'. The actor is always the user.
func (l *Ledger) resolveDisagreement(id int64, resolution, note, ownTitle, ownDescription string) error {
	d, err := l.require("disagreements", id)
	if err != nil {
		return err
	}
	switch {
	case d.str("status") != "open":
		return ledgerErr("disagreement is already closed")
	case resolution != "accept" && resolution != "reject" && resolution != "own" && resolution != "defer":
		return ledgerErr("resolution must be accept, reject, own, or defer")
	case resolution == "own" && strings.TrimSpace(ownTitle) == "":
		return ledgerErr("your own alternative needs a title")
	}
	pid := d.num("problem_id")
	return l.tx(func(x *tx) {
		var altID any
		if resolution == "own" {
			aid := x.insert("alternatives", Row{"problem_id": pid, "title": ownTitle, "description": ownDescription,
				"author": "user", "created_at": now()})
			altID = aid
			x.event("user", "add_alternative", pid, D{"alternative_id": aid, "title": ownTitle})
		}
		status := "resolved"
		if resolution == "defer" {
			status = "deferred"
		}
		x.exec("UPDATE disagreements SET status = ?, resolution = ?, resolution_note = ?, "+
			"resolved_alternative_id = ?, resolved_at = ? WHERE id = ?", status, resolution, note, altID, now(), id)
		x.event("user", "resolve_disagreement", pid, D{"disagreement_id": id, "resolution": resolution, "note": note})
	})
}

// -- selection, approval, implementation -----------------------------------

// selectAlternative with a nil alternativeID defers the problem.
func (l *Ledger) selectAlternative(problemID int64, alternativeID *int64, rationale string) (int64, error) {
	if _, err := l.require("problems", problemID); err != nil {
		return 0, err
	}
	if err := l.checkAlternative(problemID, alternativeID); err != nil {
		return 0, err
	}
	if strings.TrimSpace(rationale) == "" {
		return 0, ledgerErr("a rationale is required")
	}
	action := "select"
	if alternativeID == nil {
		action = "defer"
	}
	var id int64
	err := l.tx(func(x *tx) {
		id = x.insert("selections", Row{"problem_id": problemID, "alternative_id": opt(alternativeID),
			"rationale": rationale, "created_at": now()})
		x.event("user", action, problemID, D{"selection_id": id, "alternative_id": opt(alternativeID), "rationale": rationale})
	})
	return id, err
}

func (l *Ledger) approve(problemID int64, note string) (int64, error) {
	status, selection, _, err := l.problemState(problemID)
	if err != nil {
		return 0, err
	}
	if status != "selected" {
		return 0, ledgerErr("only a selected solution can be approved (status is %s)", status)
	}
	var id int64
	err = l.tx(func(x *tx) {
		id = x.insert("approvals", Row{"problem_id": problemID, "selection_id": selection.num("id"),
			"note": note, "created_at": now()})
		x.event("user", "approve", problemID, D{"approval_id": id, "selection_id": selection.num("id")})
	})
	return id, err
}

type Option struct{ Title, Description string }

type Payload struct {
	ProblemID    int64
	Path         []string
	Title        string
	Context      string
	Solution     Option
	Rationale    string
	ApprovalNote string
	Resolved     []struct{ Summary, Resolution, Note string }
	Decisions    []string // the user's call per agreement-board point
	OutOfScope   []Option
}

func (l *Ledger) implementationPayload(problemID int64) (*Payload, error) {
	status, selection, approval, err := l.problemState(problemID)
	if err != nil {
		return nil, err
	}
	if status != "approved" && status != "sent" {
		return nil, ledgerErr("problem %d is not approved (status is %s)", problemID, status)
	}
	problem, err := l.require("problems", problemID)
	if err != nil {
		return nil, err
	}
	alternatives, err := l.all("SELECT * FROM alternatives WHERE problem_id = ? ORDER BY id", problemID)
	if err != nil {
		return nil, err
	}
	resolved, err := l.all("SELECT * FROM disagreements WHERE problem_id = ? AND status = 'resolved' ORDER BY id", problemID)
	if err != nil {
		return nil, err
	}
	ancestors, err := l.ancestors(problemID)
	if err != nil {
		return nil, err
	}
	p := &Payload{ProblemID: problemID, Title: problem.str("title"), Context: problem.str("context"),
		Rationale: selection.str("rationale"), ApprovalNote: approval.str("note")}
	for _, a := range ancestors {
		p.Path = append(p.Path, a.str("title"))
	}
	found := false
	for _, a := range alternatives {
		if a.num("id") == selection.num("alternative_id") {
			p.Solution, found = Option{a.str("title"), a.str("description")}, true
		} else {
			p.OutOfScope = append(p.OutOfScope, Option{a.str("title"), a.str("description")})
		}
	}
	if !found {
		return nil, ledgerErr("selected alternative of problem %d no longer exists", problemID)
	}
	for _, d := range resolved {
		p.Resolved = append(p.Resolved, struct{ Summary, Resolution, Note string }{
			d.str("summary"), d.str("resolution"), d.str("resolution_note")})
	}
	return p, nil
}

func (l *Ledger) markSent(problemID int64, output, agent string, meta D) error {
	_, _, approval, err := l.problemState(problemID)
	if err != nil {
		return err
	}
	if approval == nil {
		return ledgerErr("problem %d has no current approval", problemID)
	}
	return l.tx(func(x *tx) {
		x.exec("UPDATE approvals SET sent_at = ? WHERE id = ?", now(), approval.num("id"))
		var metaText any
		if meta != nil {
			b, _ := json.Marshal(meta)
			metaText = string(b)
		}
		x.insert("contributions", Row{"problem_id": problemID, "author": agent, "kind": "implementation",
			"body": output, "created_at": now(), "meta": metaText})
		x.event("user", "send_for_implementation", problemID, D{"approval_id": approval.num("id"), "agent": agent})
	})
}

// -- import / export -------------------------------------------------------

type Export struct {
	Format     int              `json:"format"`
	ExportedAt string           `json:"exported_at,omitempty"`
	Tables     map[string][]Row `json:"tables"`
}

func (l *Ledger) export() (*Export, error) {
	e := &Export{Format: exportFormat, ExportedAt: now(), Tables: map[string][]Row{}}
	for _, t := range dataTables {
		rows, err := l.all("SELECT * FROM " + t + " ORDER BY id")
		if err != nil {
			return nil, err
		}
		e.Tables[t] = rows
	}
	return e, nil
}

func (l *Ledger) importData(data *Export) error {
	if data.Format != exportFormat {
		return ledgerErr("unsupported export format: %d", data.Format)
	}
	existing, err := l.one("SELECT id FROM problems LIMIT 1")
	if err != nil {
		return err
	}
	if existing != nil {
		return ledgerErr("import requires an empty ledger")
	}
	// Column names come from the file, so keep only real columns before building SQL.
	columns := map[string]map[string]bool{}
	for _, table := range dataTables {
		info, err := l.all("PRAGMA table_info(" + table + ")")
		if err != nil {
			return err
		}
		columns[table] = map[string]bool{}
		for _, c := range info {
			columns[table][c.str("name")] = true
		}
	}
	return l.tx(func(x *tx) {
		for _, table := range dataTables {
			for _, row := range data.Tables[table] {
				clean := Row{}
				for k, v := range row {
					if columns[table][k] {
						clean[k] = v
					}
				}
				x.insert(table, clean)
			}
		}
		x.event("user", "import", nil, D{"problems": len(data.Tables["problems"])})
	})
}

// parseExport reads an export file. Numbers stay exact: integers must not become floats.
func parseExport(raw []byte) (*Export, error) {
	var data Export
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		return nil, ledgerErr("invalid export file: %v", err)
	}
	for _, rows := range data.Tables {
		for _, row := range rows {
			for k, v := range row {
				if n, ok := v.(json.Number); ok {
					if i, err := n.Int64(); err == nil {
						row[k] = i
					} else if f, err := n.Float64(); err == nil {
						row[k] = f
					}
				}
			}
		}
	}
	return &data, nil
}
