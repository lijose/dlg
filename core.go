// Operations shared by the CLI, the MCP server and the HTTP API. Core has two
// implementations (direct SQLite, or a running `dlg serve` over HTTP) that must answer
// identically; ledger_test.go checks that.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type ProblemSummary struct {
	ID                int64  `json:"id"`
	ParentID          *int64 `json:"parent_id"`
	Title             string `json:"title"`
	Status            string `json:"status"`
	OpenDisagreements int64  `json:"open_disagreements"`
}

type PeerStatus struct {
	To    string `json:"to"`
	State string `json:"state"` // unsent | changed | sent | replied
	Mode  string `json:"mode"`
}

type ReplyRef struct {
	ID     int64  `json:"id"`
	Author string `json:"author"`
	New    bool   `json:"new"`    // arrived after the latest analysis
	Parsed bool   `json:"parsed"` // per-point positions were extracted
}

// StatusOut is the one-screen view of a problem: where it stands and who we wait for.
type StatusOut struct {
	ID         int64        `json:"id"`
	Title      string       `json:"title"`
	Status     string       `json:"status"`
	Round      int          `json:"round"`
	Points     int          `json:"points"`
	Settled    int          `json:"settled"`
	Disputed   int          `json:"disputed"`
	Reviewers  []PeerStatus `json:"reviewers"`
	Waiting    []string     `json:"waiting"` // handoff sent, no reply yet
	ToSend     []string     `json:"to_send"` // nothing sent yet, or an update is ready
	NewReplies int          `json:"new_replies"`
	Replies    []ReplyRef   `json:"replies"`
	Cost       *CostOut     `json:"cost,omitempty"`
}

// CostLine sums the usage of some runs. Priced counts the runs that reported a cost, so a
// total over runs without one (cursor-agent) is not mistaken for free.
type CostLine struct {
	Name         string  `json:"name,omitempty"`
	Runs         int     `json:"runs"`
	Priced       int     `json:"priced"`
	Failed       int     `json:"failed,omitempty"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CacheRead    int64   `json:"cache_read_tokens"`
	CacheWrite   int64   `json:"cache_write_tokens"`
	DurationMS   int64   `json:"duration_ms"`
}

type CostOut struct {
	Total   CostLine   `json:"total"`
	ByAgent []CostLine `json:"by_agent"`
	ByModel []CostLine `json:"by_model"`
}

func (c *CostLine) add(u Usage, share int64, failed bool) {
	c.Runs++
	if failed {
		c.Failed++
	}
	if u.CostUSD != nil {
		c.Priced++
		c.CostUSD += *u.CostUSD / float64(share)
	}
	c.InputTokens += u.InputTokens / share
	c.OutputTokens += u.OutputTokens / share
	c.CacheRead += u.CacheRead / share
	c.CacheWrite += u.CacheWrite / share
	c.DurationMS += u.DurationMS / share
}

// costOf totals the problem's "usage" audit events (failed runs included; a run shared by
// several problems counts its share). Nil when no run reported usage.
func costOf(d *Detail) *CostOut {
	out := &CostOut{}
	agents, models := map[string]*CostLine{}, map[string]*CostLine{}
	line := func(m map[string]*CostLine, k string) *CostLine {
		if m[k] == nil {
			m[k] = &CostLine{Name: k}
		}
		return m[k]
	}
	for _, e := range d.Events {
		if e.str("action") != "usage" {
			continue
		}
		var m struct {
			Usage  Usage `json:"usage"`
			Model  string
			Shared int64
			Failed bool
		}
		if json.Unmarshal([]byte(e.str("detail")), &m) != nil {
			continue
		}
		share := max(m.Shared, 1)
		// The user's choice and the model the agent reports it used, when it reports one.
		model := orDefault(m.Model, "unknown")
		if actual := strings.Join(m.Usage.Models, " + "); actual != "" {
			model += " → " + actual
		}
		for _, c := range []*CostLine{&out.Total, line(agents, e.str("actor")), line(models, model)} {
			c.add(m.Usage, share, m.Failed)
		}
	}
	if out.Total.Runs == 0 {
		return nil
	}
	sorted := func(m map[string]*CostLine) []CostLine {
		lines := []CostLine{}
		for _, k := range slices.Sorted(maps.Keys(m)) {
			lines = append(lines, *m[k])
		}
		return lines
	}
	out.ByAgent, out.ByModel = sorted(agents), sorted(models)
	return out
}

type ReviewOut struct {
	ID       int64    `json:"id"`
	Warnings []string `json:"warnings"`
}

type ReplyPoint struct {
	Key    string `json:"key"`
	Stance string `json:"stance"` // AGREE | DISAGREE
	Reason string `json:"reason"`
}

// ParsedReply is a reviewer's reply split along the requested reply format.
type ParsedReply struct {
	ReviewID int64        `json:"review_id"`
	Author   string       `json:"author"`
	Summary  string       `json:"summary"`
	Solution string       `json:"solution"`
	Points   []ReplyPoint `json:"points"`
	New      string       `json:"new"`
	Open     string       `json:"open"`
	Warnings []string     `json:"warnings"`
}

// Core is everything an agent or script can do. Implemented by localCore and remoteCore.
type Core interface {
	Settings() (Settings, error)
	Problems() ([]ProblemSummary, error)
	Status(pid int64) (*StatusOut, error)
	// Handoff returns the next text for a reviewer; record marks it as sent.
	Handoff(pid int64, to string, record bool) (*Handoff, error)
	Review(pid int64, author, body string, meta D) (*ReviewOut, error)
	Note(pid int64, body string) (int64, error)
	Parse(reviewID int64) (*ParsedReply, error)
}

// -- shared logic ----------------------------------------------------------

func problemSummaries(l *Ledger) ([]ProblemSummary, error) {
	rows, err := l.listProblems()
	out := []ProblemSummary{}
	for _, r := range rows {
		p := ProblemSummary{ID: r.num("id"), Title: r.str("title"), Status: r.str("status"), OpenDisagreements: r.num("open_disagreements")}
		if !r.isNil("parent_id") {
			p.ParentID = ptr(r.num("parent_id"))
		}
		out = append(out, p)
	}
	return out, err
}

func problemStatus(l *Ledger, pid int64) (*StatusOut, error) {
	d, err := (&server{l}).detailWithHandoffs(pid)
	if err != nil {
		return nil, err
	}
	out := &StatusOut{ID: pid, Title: d.Problem.str("title"), Status: d.Status,
		Reviewers: []PeerStatus{}, Waiting: []string{}, ToSend: []string{}, Replies: []ReplyRef{}}
	latest, analysis, round := latestAnalysis(d)
	out.Round = round
	out.Cost = d.Cost
	if analysis != nil {
		out.Points = len(analysis.Points)
		ruled := rulings(d)
		for _, p := range analysis.Points {
			switch {
			case settled(p, ruled):
				out.Settled++
			case p.Status == "disputed":
				out.Disputed++
			}
		}
	}
	for _, h := range d.Handoffs {
		out.Reviewers = append(out.Reviewers, PeerStatus{h.To, h.State, h.Mode})
		switch h.State {
		case "sent":
			out.Waiting = append(out.Waiting, h.To)
		case "unsent", "changed":
			out.ToSend = append(out.ToSend, h.To)
		}
	}
	for _, r := range ofKind(d, "review") {
		ref := ReplyRef{ID: r.num("id"), Author: r.str("author"), New: latest != nil && r.num("id") > latest.num("id")}
		var meta D
		if json.Unmarshal([]byte(r.str("meta")), &meta) == nil {
			_, ref.Parsed = meta["parsed"]
		}
		if ref.New {
			out.NewReplies++
		}
		out.Replies = append(out.Replies, ref)
	}
	return out, nil
}

func peekHandoff(l *Ledger, pid int64, to string) (*Handoff, error) {
	d, err := (&server{l}).detailWithHandoffs(pid)
	if err != nil {
		return nil, err
	}
	for _, h := range d.Handoffs {
		if h.To == to {
			return &h, nil
		}
	}
	return nil, ledgerErr("%q is not a configured secondary agent, or there is no analysis yet", to)
}

var dangling = regexp.MustCompile(`(?:[,:;\-–—(]|\.\.\.|…)\s*$`)

// replyWarnings flags replies that are probably unusable. The reply is stored either way.
func replyWarnings(body string, meta D) []string {
	warnings := []string{}
	if truncated, _ := meta["truncated"].(bool); truncated {
		warnings = append(warnings, "the reply was cut at the size limit")
	}
	if strings.Count(body, fence)%2 == 1 || dangling.MatchString(body) {
		warnings = append(warnings, "the reply looks truncated (it ends mid-sentence or inside a code block)")
	}
	if p := parseReply(body); len(p.Points) == 0 {
		warnings = append(warnings, "no \"[P1] AGREE|DISAGREE: why\" lines found; per-point positions cannot be extracted")
	}
	return warnings
}

// addReview stores a reply verbatim. Warnings are advice, never a reason to refuse it.
func addReview(l *Ledger, pid int64, author, body string, meta D) (*ReviewOut, error) {
	if strings.TrimSpace(author) == "" || strings.TrimSpace(body) == "" {
		return nil, ledgerErr("a reviewer and a non-empty response are required")
	}
	id, err := l.addContributionMeta(pid, author, "review", body, meta)
	if err != nil {
		return nil, err
	}
	return &ReviewOut{ID: id, Warnings: replyWarnings(body, meta)}, nil
}

func addNote(l *Ledger, pid int64, body string) (int64, error) {
	if strings.TrimSpace(body) == "" {
		return 0, ledgerErr("a note is required")
	}
	return l.addContribution(pid, "user", "context", body)
}

var (
	replyPointRe   = regexp.MustCompile(`(?i)^[\s>*_•\-]*\[([^\]\s]+)\][\s*_]*(AGREE|DISAGREE)\b[\s*_]*[:\-–—]?\s*(.*)$`)
	replySectionRe = regexp.MustCompile(`^[\s>*_#\-]*(SUMMARY|SOLUTION|NEW|OPEN)[\s*_]*:[\s*_]*(.*)$`)
)

// parseReply reads the reply format of replyFormat. Free text it cannot place is attached
// to the section above it.
func parseReply(body string) ParsedReply {
	out := ParsedReply{Points: []ReplyPoint{}, Warnings: []string{}}
	var cur *string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" && cur != nil {
			if *cur != "" {
				*cur += "\n"
			}
			*cur += s
		}
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := replyPointRe.FindStringSubmatch(line); m != nil {
			out.Points = append(out.Points, ReplyPoint{Key: m[1], Stance: strings.ToUpper(m[2]), Reason: strings.TrimSpace(m[3])})
			cur = &out.Points[len(out.Points)-1].Reason
			continue
		}
		if m := replySectionRe.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "SUMMARY":
				cur = &out.Summary
			case "SOLUTION":
				cur = &out.Solution
			case "NEW":
				cur = &out.New
			default:
				cur = &out.Open
			}
			add(m[2])
			continue
		}
		add(line)
	}
	return out
}

// parseReview extracts per-point positions from a stored reply and keeps the result in its metadata.
func parseReview(l *Ledger, reviewID int64) (*ParsedReply, error) {
	c, err := l.require("contributions", reviewID)
	if err != nil {
		return nil, err
	}
	if c.str("kind") != "review" {
		return nil, ledgerErr("contribution %d is not a review", reviewID)
	}
	p := parseReply(c.str("body"))
	p.ReviewID, p.Author = reviewID, c.str("author")
	p.Warnings = replyWarnings(c.str("body"), nil)
	if err := l.mergeMeta(reviewID, D{"parsed": p}); err != nil {
		return nil, err
	}
	return &p, nil
}

// -- direct SQLite ---------------------------------------------------------

type localCore struct{ l *Ledger }

func (c localCore) Settings() (Settings, error)                { return c.l.settings() }
func (c localCore) Problems() ([]ProblemSummary, error)        { return problemSummaries(c.l) }
func (c localCore) Status(pid int64) (*StatusOut, error)       { return problemStatus(c.l, pid) }
func (c localCore) Parse(id int64) (*ParsedReply, error)       { return parseReview(c.l, id) }
func (c localCore) Note(pid int64, body string) (int64, error) { return addNote(c.l, pid, body) }

func (c localCore) Review(pid int64, author, body string, meta D) (*ReviewOut, error) {
	return addReview(c.l, pid, author, body, meta)
}

func (c localCore) Handoff(pid int64, to string, record bool) (*Handoff, error) {
	h, err := peekHandoff(c.l, pid, to)
	if err != nil || !record {
		return h, err
	}
	if _, err := (&server{c.l}).recordHandoff(pid, to); err != nil {
		return nil, err
	}
	return h, nil
}

// -- running `dlg serve` over HTTP ----------------------------------------

type remoteCore struct {
	base   string
	client *http.Client
}

// call sends one API request; rule violations (400) come back as LedgerError, like local calls.
func (c remoteCore) call(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ledger", "1")
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		var e struct {
			Detail string `json:"detail"`
		}
		json.NewDecoder(res.Body).Decode(&e)
		if e.Detail == "" {
			e.Detail = res.Status
		}
		if res.StatusCode == http.StatusBadRequest {
			return LedgerError(e.Detail)
		}
		return errors.New(e.Detail)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func pidPath(pid int64, suffix string) string { return "/api/problems/" + itoa(pid) + suffix }

func (c remoteCore) Settings() (Settings, error) {
	var out struct {
		Settings Settings `json:"settings"`
	}
	err := c.call("GET", "/api/state", nil, &out)
	return out.Settings, err
}

func (c remoteCore) Problems() ([]ProblemSummary, error) {
	var out struct {
		Problems []ProblemSummary `json:"problems"`
	}
	err := c.call("GET", "/api/state", nil, &out)
	if out.Problems == nil {
		out.Problems = []ProblemSummary{}
	}
	return out.Problems, err
}

func (c remoteCore) Status(pid int64) (*StatusOut, error) {
	var out StatusOut
	return &out, c.call("GET", pidPath(pid, "/status"), nil, &out)
}

func (c remoteCore) Handoff(pid int64, to string, record bool) (*Handoff, error) {
	var h Handoff
	if err := c.call("GET", pidPath(pid, "/handoffs/"+url.PathEscape(to)), nil, &h); err != nil {
		return nil, err
	}
	if record {
		if err := c.call("POST", pidPath(pid, "/handoffs"), D{"to": to}, nil); err != nil {
			return nil, err
		}
	}
	return &h, nil
}

func (c remoteCore) Review(pid int64, author, body string, meta D) (*ReviewOut, error) {
	var out ReviewOut
	err := c.call("POST", pidPath(pid, "/reviews"), D{"author": author, "body": body, "meta": meta}, &out)
	return &out, err
}

func (c remoteCore) Note(pid int64, body string) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	err := c.call("POST", pidPath(pid, "/notes"), D{"body": body}, &out)
	return out.ID, err
}

func (c remoteCore) Parse(id int64) (*ParsedReply, error) {
	var out ParsedReply
	return &out, c.call("POST", "/api/reviews/"+itoa(id)+"/parse", nil, &out)
}
