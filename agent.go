// Primary-agent CLI adapter. The ledger contains no model: it runs an installed,
// logged-in CLI (e.g. `codex exec`, `claude -p`) with the prompt on stdin.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"time"
)

const agentTimeout = 1800 * time.Second

// AgentError means the agent CLI failed or returned output the ledger cannot use.
type AgentError string

func (e AgentError) Error() string { return string(e) }

// -- structured output the agent must return (validated: agent output is untrusted) --

type EvidenceOut struct {
	Quote  string `json:"quote"`
	Source string `json:"source"`
}

type AlternativeOut struct {
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Evidence    []EvidenceOut `json:"evidence"`
}

type SubproblemOut struct {
	Title   string `json:"title"`
	Context string `json:"context"`
}

type QuestionOut struct {
	Question   string `json:"question"`
	Assumption string `json:"assumption"`
}

// PointOut is one row of the agreement board. Keys stay stable across rounds.
type PointOut struct {
	Key       string            `json:"key"`
	Topic     string            `json:"topic"`
	Primary   string            `json:"primary"`
	Positions map[string]string `json:"positions"` // secondary name -> its position
	Status    string            `json:"status"`    // agreed | disputed | open | decided
}

type AnalysisOut struct {
	Summary      string           `json:"summary"`
	Changes      []string         `json:"changes"`
	Questions    []QuestionOut    `json:"questions"`
	Subproblems  []SubproblemOut  `json:"subproblems"`
	Solution     AlternativeOut   `json:"solution"`
	Alternatives []AlternativeOut `json:"alternatives"`
	OpenPoints   []string         `json:"open_points"`
	Points       []PointOut       `json:"points"`
}

type FinalOut struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Rationale   string `json:"rationale"`
}

// pointKeyRe keeps board keys short safe tokens (P1, auth.timeout): they end up in the page.
var pointKeyRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)

func (a AlternativeOut) validate() error {
	if a.Title == "" {
		return errors.New("alternative without title")
	}
	for _, e := range a.Evidence {
		if e.Quote == "" {
			return errors.New("evidence without quote")
		}
	}
	return nil
}

func (p *AnalysisOut) validate() error {
	if p.Summary == "" {
		return errors.New("analysis without summary")
	}
	if err := p.Solution.validate(); err != nil {
		return fmt.Errorf("solution: %w", err)
	}
	for _, q := range p.Questions {
		if q.Question == "" {
			return errors.New("empty question")
		}
	}
	for _, s := range p.Subproblems {
		if s.Title == "" {
			return errors.New("subproblem without title")
		}
	}
	for _, a := range p.Alternatives {
		if err := a.validate(); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, pt := range p.Points {
		switch {
		case pt.Key == "" || pt.Topic == "":
			return errors.New("point without key or topic")
		case !pointKeyRe.MatchString(pt.Key):
			return fmt.Errorf("bad point key %q (use letters, digits, '.', '_' or '-')", pt.Key)
		case seen[pt.Key]:
			return fmt.Errorf("duplicate point key %q", pt.Key)
		case pt.Status != "agreed" && pt.Status != "disputed" && pt.Status != "open" && pt.Status != "decided":
			return fmt.Errorf("bad point status %q", pt.Status)
		}
		seen[pt.Key] = true
	}
	return nil
}

func (f *FinalOut) validate() error {
	if f.Title == "" || f.Description == "" {
		return errors.New("final proposal without title or description")
	}
	return nil
}

// runAgent runs command with the prompt on stdin. A long prompt would hit the OS argument
// limit as an argument, and one starting with "-" would be read as a flag.
func runAgent(command, prompt, cwd string) (string, error) {
	out, _, err := runAgentCtx(context.Background(), command, prompt, cwd, nil)
	return out, err
}

// runAgentCtx is runAgent that can be cancelled through parent and mirrors stdout and
// stderr to live (a mutex-guarded tailBuffer) while the agent runs. Usage is nil unless the
// agent prints stream-json with a result event.
func runAgentCtx(parent context.Context, command, prompt, cwd string, live io.Writer) (string, *Usage, error) {
	args, err := splitCommand(command)
	if err != nil {
		return "", nil, AgentError(err.Error())
	}
	if len(args) == 0 {
		return "", nil, AgentError("no agent command configured")
	}
	if streamed := withStreaming(args); len(streamed) != len(args) {
		args = streamed
		if live != nil {
			fmt.Fprintf(live, "$ %s\n", strings.Join(args, " "))
		}
	}
	ctx, cancel := context.WithTimeout(parent, agentTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(prompt)
	cmd.WaitDelay = 5 * time.Second // don't hang on grandchildren holding the pipes
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if live != nil {
		cmd.Stdout, cmd.Stderr = io.MultiWriter(&stdout, live), io.MultiWriter(&stderr, live)
	} else {
		live = io.Discard
	}
	var stream *streamReader
	if isStreamJSON(args) { // show the events as readable progress; the result text is the output
		stream = &streamReader{progress: live}
		cmd.Stdout = io.MultiWriter(&stdout, stream)
	}
	err = cmd.Run()
	if stream != nil {
		stream.flush()
	}
	switch {
	case errors.Is(parent.Err(), context.Canceled):
		return "", nil, AgentError("cancelled")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", nil, AgentError(fmt.Sprintf("agent timed out after %ds", int(agentTimeout.Seconds())))
	case errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist):
		return "", nil, AgentError("command not found: " + args[0])
	case err != nil:
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if r := []rune(msg); len(r) > 2000 {
			msg = string(r[len(r)-2000:])
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", nil, AgentError(msg)
	}
	if stream != nil {
		if stream.result == nil || stream.failed {
			return "", stream.usage, AgentError("the agent ended without a result event")
		}
		return *stream.result, stream.usage, nil
	}
	return stdout.String(), nil, nil
}

// splitCommand splits like a POSIX shell: whitespace separates words; single quotes are
// literal; double quotes allow backslash escapes; a backslash escapes the next character.
func splitCommand(s string) ([]string, error) {
	var args []string
	var word strings.Builder
	inWord := false
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; {
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				args, word, inWord = append(args, word.String()), strings.Builder{}, false
			}
		case c == '\'' || c == '"':
			inWord = true
			i++
			for ; i < len(rs) && rs[i] != c; i++ {
				if c == '"' && rs[i] == '\\' && i+1 < len(rs) && (rs[i+1] == '"' || rs[i+1] == '\\') {
					i++
				}
				word.WriteRune(rs[i])
			}
			if i == len(rs) {
				return nil, errors.New("agent command has an unterminated quote")
			}
		case c == '\\' && i+1 < len(rs):
			inWord = true
			i++
			word.WriteRune(rs[i])
		default:
			inWord = true
			word.WriteRune(c)
		}
	}
	if inWord {
		args = append(args, word.String())
	}
	return args, nil
}

var fenced = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

// parseAgent reads the last valid JSON object in the output, preferring fenced ```json blocks.
func parseAgent[T any, P interface {
	*T
	validate() error
}](text string) (*T, error) {
	var candidates []string
	for _, m := range fenced.FindAllStringSubmatch(text, -1) {
		candidates = append([]string{m[1]}, candidates...)
	}
	if first, last := strings.Index(text, "{"), strings.LastIndex(text, "}"); len(candidates) == 0 && first >= 0 && last > first {
		candidates = []string{text[first : last+1]}
	}
	for _, c := range candidates {
		var v T
		if json.Unmarshal([]byte(c), &v) == nil && P(&v).validate() == nil {
			return &v, nil
		}
	}
	return nil, AgentError("agent output has no valid JSON block; the raw output was kept")
}

// -- prompts ---------------------------------------------------------------

const fence = "```"

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func describe(d *Detail) string {
	p, path := d.Problem, problemPath(d)
	var notes, alts, subs []string
	for _, c := range d.Contributions {
		if c.str("kind") == "context" {
			notes = append(notes, "- "+c.str("body"))
		}
	}
	for _, a := range d.Alternatives {
		alts = append(alts, fmt.Sprintf("- [%d] %s (by %s): %s", a.num("id"), a.str("title"), a.str("author"), a.str("description")))
	}
	for _, c := range d.Children {
		subs = append(subs, "- "+c.str("title"))
	}
	parts := []string{"Problem: " + path, "Context:\n" + orDefault(p.str("context"), "(none)")}
	if len(notes) > 0 {
		parts = append(parts, "User notes:\n"+strings.Join(notes, "\n"))
	}
	parts = append(parts,
		"Current alternatives:\n"+orDefault(strings.Join(alts, "\n"), "(none yet)"),
		"Current subproblems:\n"+orDefault(strings.Join(subs, "\n"), "(none)"))
	return strings.Join(parts, "\n\n")
}

const analysisSchema = `{"summary": "...", "changes": ["what changed since the previous round"],
 "questions": [{"question": "...", "assumption": "what you assumed meanwhile"}],
 "subproblems": [{"title": "...", "context": "..."}],
 "solution": {"title": "...", "description": "...", "evidence": [{"quote": "...", "source": "path:line"}]},
 "alternatives": [{"title": "...", "description": "why not chosen"}],
 "open_points": ["..."],
 "points": [{"key": "P1", "topic": "...", "primary": "your position", "positions": {"<reviewer name>": "their position"}, "status": "agreed|disputed|open|decided"}]}`

// analysePrompt covers the first analysis and every later round: prev is the last round's
// position (nil on the first), replies are reviewer answers pasted since then.
func analysePrompt(d *Detail, prev *AnalysisOut, replies []Row) string {
	parts := []string{`You are the primary engineering agent working with a decision ledger. Investigate the code in the current directory as needed. Do not modify any files.

Your analysis is shown to the user and copied to independent reviewers (other AI models) who cannot see the code, so make it self-contained: quote the code facts that matter.`,
		describe(d)}
	if prev != nil {
		b, _ := json.Marshal(prev)
		parts = append(parts, "Your position in the previous round:\n"+string(b))
	}
	for _, r := range replies {
		parts = append(parts, fmt.Sprintf("Reply from %s (verbatim):\n<<<\n%s\n>>>", r.str("author"), r.str("body")))
	}
	parts = append(parts, fmt.Sprintf(`Do the following:
- Summarise the problem and analyse it. Ask clarifying questions where the answer would change the solution; for each, state the assumption you made meanwhile. Drop questions the user notes already answer.
- Suggest subproblems only if the problem is too large to decide in one step.
- Recommend one solution, backed by evidence quoted from the code (source as path:line), and list the other options you considered.
- List the open points.
- Keep an agreement board of the points that matter for the decision. Keep existing keys stable and add new keys for new points. For each reviewer who replied, record their position under positions[their name], keeping earlier positions. Reviewers are sent only the open and disputed points (settled ones are listed by key), so a reply that skips a point leaves that point's positions and status as they were. Status: agreed when every model that weighed in agrees, disputed when one disagrees, open when it needs more information, decided when a user note rules on it.
- Adopt a reviewer's suggestion when it is better, and say so. List what changed since the previous round in changes.

End your answer with exactly one fenced %sjson block:
%s`, fence, analysisSchema))
	return strings.Join(parts, "\n\n")
}

func finalPrompt(d *Detail, a *AnalysisOut) string {
	b, _ := json.Marshal(a)
	return fmt.Sprintf(`You are the primary engineering agent. Write the final solution proposal for this problem. Do not modify any files.

%s

Latest agreed position, with each reviewer's stance per point:
%s

Build the proposal from the agreed points and the user's rulings (decided points). Name any point still disputed or open and how the proposal handles it. The description is the full proposal in markdown: approach, key steps, risks, and what is out of scope.

End your answer with exactly one fenced %sjson block:
{"title": "...", "description": "...", "rationale": "why this is the solution, in one or two sentences"}`, describe(d), string(b), fence)
}

// -- handoff text: what the user copies to a secondary model ------------------

const replyFormat = `---
Please reply in this format:
SUMMARY: your view in 2-3 lines
SOLUTION: the approach you recommend (say if it matches mine)
[P1] AGREE | DISAGREE: why   (one line per point for review)
NEW: important points I missed (optional)
OPEN: questions you need answered (optional)`

func problemPath(d *Detail) string {
	titles := []string{}
	for _, a := range d.Ancestors {
		titles = append(titles, a.str("title"))
	}
	return strings.Join(append(titles, d.Problem.str("title")), " > ")
}

func bullets(items []string) string {
	return "- " + strings.Join(items, "\n- ")
}

// solutionText quotes the evidence for a reviewer that cannot see the code, and only
// points to it for one that can.
func solutionText(s AlternativeOut, hasCode bool) string {
	lines := []string{s.Title, s.Description}
	for _, e := range s.Evidence {
		if hasCode {
			lines = append(lines, "  see "+orDefault(e.Source, e.Quote))
		} else {
			lines = append(lines, fmt.Sprintf("  > %s (%s)", e.Quote, orDefault(e.Source, "no source")))
		}
	}
	return strings.Join(lines, "\n")
}

const codeAccessNote = "You have this repository open. Check my claims against the code yourself rather than taking them on trust; sources are given as path:line."

// pointLine shows the primary's position and every other reviewer's; the recipient
// already knows its own.
func pointLine(p PointOut, primary, recipient string) string {
	line := fmt.Sprintf("[%s] %s (%s)\n  %s: %s", p.Key, p.Topic, p.Status, primary, p.Primary)
	for name, pos := range p.Positions {
		if name != recipient {
			line += fmt.Sprintf("\n  %s: %s", name, pos)
		}
	}
	return line
}

// needsReview reports whether a point still wants a reviewer's input: open or disputed,
// with no user ruling. Agreed, decided and ruled points are only listed, not argued again.
func needsReview(p PointOut, ruled map[string]string) bool {
	_, byUser := ruled[p.Key]
	return !byUser && (p.Status == "open" || p.Status == "disputed")
}

func settledLine(p PointOut, ruled map[string]string) string {
	status := p.Status
	if _, ok := ruled[p.Key]; ok {
		status = "user ruling"
	}
	return fmt.Sprintf("[%s] %s (%s)", p.Key, p.Topic, status)
}

// othersSee is what pointLine shows the recipient: everything but its own position, which
// the primary rewrites every round and must not make a point look changed.
func othersSee(p PointOut, recipient string) PointOut {
	p.Positions = maps.Clone(p.Positions)
	delete(p.Positions, recipient)
	return p
}

func optionLines(alts []AlternativeOut) []string {
	lines := []string{}
	for _, o := range alts {
		lines = append(lines, o.Title+": "+o.Description)
	}
	return lines
}

func questionLines(qs []QuestionOut) []string {
	lines := []string{}
	for _, q := range qs {
		lines = append(lines, fmt.Sprintf("%s (assumed: %s)", q.Question, orDefault(q.Assumption, "nothing")))
	}
	return lines
}

func fullHandoff(d *Detail, a *AnalysisOut, primary, recipient string, hasCode bool, round int, notes []string) string {
	parts := []string{"# Problem: " + problemPath(d)}
	if hasCode {
		parts = append(parts, codeAccessNote)
	}
	parts = append(parts, "## Context\n"+orDefault(d.Problem.str("context"), "(none)"))
	if len(notes) > 0 {
		parts = append(parts, "## Notes from the user\n"+bullets(notes))
	}
	parts = append(parts, fmt.Sprintf("## %s's analysis (round %d)\n%s", primary, round, a.Summary),
		"### Proposed solution: "+solutionText(a.Solution, hasCode))
	if len(a.Alternatives) > 0 {
		parts = append(parts, "### Other options considered\n"+bullets(optionLines(a.Alternatives)))
	}
	if len(a.Questions) > 0 {
		parts = append(parts, "### Open questions\n"+bullets(questionLines(a.Questions)))
	}
	if len(a.OpenPoints) > 0 {
		parts = append(parts, "### Open points\n"+bullets(a.OpenPoints))
	}
	ruled := rulings(d)
	points, closed := []string{}, []string{}
	for _, p := range a.Points {
		if needsReview(p, ruled) {
			points = append(points, pointLine(p, primary, recipient))
		} else {
			closed = append(closed, settledLine(p, ruled))
		}
	}
	if len(points) > 0 {
		parts = append(parts, "### Points for your review\n"+strings.Join(points, "\n"))
	} else if len(a.Points) > 0 {
		parts = append(parts, "No points need your review. Reply with SUMMARY, and NEW only if you see a problem.")
	}
	if len(closed) > 0 {
		parts = append(parts, "### Settled, no reply needed; reply [Pn] DISAGREE: why only to reopen one\n"+strings.Join(closed, "\n"))
	}
	return strings.Join(append(parts, replyFormat), "\n\n")
}

// deltaHandoff carries only what changed since prev, the analysis the recipient last saw:
// points that still need review and changed (or reopened), and points settled since.
func deltaHandoff(d *Detail, prev, cur *AnalysisOut, primary, recipient string, hasCode bool, round int, notes, changes []string) string {
	parts := []string{fmt.Sprintf("# Update (round %d) on: %s\nOnly what changed since my last message.", round, problemPath(d))}
	if len(changes) > 0 {
		parts = append(parts, "## What changed\n"+bullets(changes))
	}
	if len(notes) > 0 {
		parts = append(parts, "## New notes from the user\n"+bullets(notes))
	}
	if prev.Solution.Title != cur.Solution.Title || prev.Solution.Description != cur.Solution.Description {
		parts = append(parts, "## Revised solution: "+solutionText(cur.Solution, hasCode))
	} else {
		parts = append(parts, "Solution unchanged: "+cur.Solution.Title)
	}
	before := map[string]PointOut{}
	for _, p := range prev.Points {
		before[p.Key] = p
	}
	// Rulings the recipient had already seen: those not among the new notes.
	ruled, fresh, ruledBefore := rulings(d), map[string]bool{}, map[string]string{}
	for _, n := range notes {
		fresh[n] = true
	}
	for _, c := range ofKind(d, "context") {
		if m := rulingRe.FindStringSubmatch(c.str("body")); m != nil && !fresh[c.str("body")] {
			ruledBefore[m[1]] = m[2]
		}
	}
	points, closed := []string{}, []string{}
	for _, p := range cur.Points {
		old, ok := before[p.Key]
		wasOpen := !ok || needsReview(old, ruledBefore)
		switch {
		case needsReview(p, ruled):
			if !wasOpen || !reflect.DeepEqual(othersSee(old, recipient), othersSee(p, recipient)) {
				points = append(points, pointLine(p, primary, recipient))
			}
		case wasOpen:
			closed = append(closed, settledLine(p, ruled))
		}
	}
	if len(points) > 0 {
		parts = append(parts, "## Points for your review\n"+strings.Join(points, "\n"))
	} else {
		parts = append(parts, "No points need your review this round. Reply with SUMMARY, and NEW only if you see a problem.")
	}
	if len(closed) > 0 {
		parts = append(parts, "## Newly settled, no reply needed; reply [Pn] DISAGREE: why only to reopen one\n"+strings.Join(closed, "\n"))
	}
	if added := newItems(optionLines(prev.Alternatives), optionLines(cur.Alternatives)); len(added) > 0 {
		parts = append(parts, "## New options considered\n"+bullets(added))
	}
	if added := newItems(questionLines(prev.Questions), questionLines(cur.Questions)); len(added) > 0 {
		parts = append(parts, "## New questions\n"+bullets(added))
	}
	if added := newItems(prev.OpenPoints, cur.OpenPoints); len(added) > 0 {
		parts = append(parts, "## New open points\n"+bullets(added))
	}
	if closed := newItems(cur.OpenPoints, prev.OpenPoints); len(closed) > 0 {
		parts = append(parts, "## Closed open points\n"+bullets(closed))
	}
	return strings.Join(append(parts, replyFormat), "\n\n")
}

// newItems returns the items of cur that are not in prev.
func newItems(prev, cur []string) []string {
	seen := map[string]bool{}
	for _, s := range prev {
		seen[s] = true
	}
	out := []string{}
	for _, s := range cur {
		if !seen[s] {
			out = append(out, s)
		}
	}
	return out
}

func implementPrompt(payloads []*Payload) string {
	sections := []string{}
	for _, p := range payloads {
		lines := []string{"## " + strings.Join(append(p.Path, p.Title), " > ")}
		if p.Context != "" {
			lines = append(lines, "Context: "+p.Context)
		}
		lines = append(lines, fmt.Sprintf("Approved solution: %s\n%s", p.Solution.Title, p.Solution.Description),
			"Rationale: "+p.Rationale)
		if p.ApprovalNote != "" {
			lines = append(lines, "Approval note: "+p.ApprovalNote)
		}
		if len(p.Decisions) > 0 {
			lines = append(lines, "Settled points (user rulings, and points the models agreed on that the user did not rule on):\n- "+strings.Join(p.Decisions, "\n- "))
		}
		if len(p.Resolved) > 0 {
			decided := []string{}
			for _, d := range p.Resolved {
				line := fmt.Sprintf("- %s -> %s", d.Summary, d.Resolution)
				if d.Note != "" {
					line += ": " + d.Note
				}
				decided = append(decided, line)
			}
			lines = append(lines, "Decided points:\n"+strings.Join(decided, "\n"))
		}
		if len(p.OutOfScope) > 0 {
			out := []string{}
			for _, a := range p.OutOfScope {
				out = append(out, "- "+a.Title)
			}
			lines = append(lines, "Out of scope (rejected alternatives, do not implement):\n"+strings.Join(out, "\n"))
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	return "The user approved the decisions below. Implement exactly these and nothing else. " +
		"Stop and report if an approved solution turns out to be infeasible.\n\n" + strings.Join(sections, "\n\n") +
		"\n\nFinish with a short summary of the changes you made."
}
