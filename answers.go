package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

type answerMeta struct {
	AnalysisID     int64  `json:"analysis_id"`
	QuestionIndex  int    `json:"question_index"`
	Answer         string `json:"answer"`
	ContributionID int64  `json:"-"`
}

func savedAnswer(d *Detail, analysisID int64, index int) *answerMeta {
	var found *answerMeta
	for _, c := range ofKind(d, "context") {
		var m answerMeta
		if json.Unmarshal([]byte(c.str("meta")), &m) == nil && m.AnalysisID == analysisID && m.QuestionIndex == index {
			copy := m
			copy.ContributionID = c.num("id")
			found = &copy
		}
	}
	if found == nil {
		for _, row := range ofKind(d, "analysis") {
			if row.num("id") == analysisID {
				if a := analysisOf(row); a != nil && index >= 0 && index < len(a.Questions) {
					prefix := "Q: " + a.Questions[index].Question + " A: "
					for _, note := range ofKind(d, "context") {
						if note.num("id") > analysisID && strings.HasPrefix(note.str("body"), prefix) {
							found = &answerMeta{AnalysisID: analysisID, QuestionIndex: index, Answer: strings.TrimPrefix(note.str("body"), prefix), ContributionID: note.num("id")}
						}
					}
				}
			}
		}
	}
	return found
}
func unansweredQuestions(d *Detail) int {
	row, a, _ := latestAnalysis(d)
	if a == nil {
		return 0
	}
	n := 0
	for i := range a.Questions {
		if savedAnswer(d, row.num("id"), i) == nil {
			n++
		}
	}
	return n
}
func (s *server) answerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/problems/{pid}/answers", withID("pid", func(pid int64, b struct {
		AnalysisID    int64    `json:"analysis_id"`
		Index         int      `json:"index"`
		Answer        string   `json:"answer"`
		UseAssumption bool     `json:"use_assumption"`
		PointKeys     []string `json:"point_keys"`
	}) (any, error) {
		d, err := s.l.problemDetail(pid)
		if err != nil {
			return nil, err
		}
		row, a, _ := latestAnalysis(d)
		if a == nil || row.num("id") != b.AnalysisID || b.Index < 0 || b.Index >= len(a.Questions) {
			return nil, ledgerErr("this question has changed; refresh before answering")
		}
		answer := strings.TrimSpace(b.Answer)
		if b.UseAssumption {
			answer = "Proceed with the stated assumption: " + a.Questions[b.Index].Assumption
		}
		if answer == "" {
			return nil, ledgerErr("enter an answer or explicitly use the assumption")
		}
		points, err := answerRulingPoints(a, b.PointKeys)
		if err != nil {
			return nil, err
		}
		var existing int64
		if previous := savedAnswer(d, b.AnalysisID, b.Index); previous != nil && previous.Answer == answer {
			existing = previous.ContributionID
		}
		id, err := s.l.saveAnswerRulings(d, b.AnalysisID, existing,
			fmt.Sprintf("Q: %s A: %s", a.Questions[b.Index].Question, answer),
			D{"analysis_id": b.AnalysisID, "question_index": b.Index, "answer": answer}, points)
		return idOf(id, err)
	}))

	mux.HandleFunc("POST /api/problems/{pid}/answer-rulings", withID("pid", func(pid int64, b struct {
		AnalysisID int64    `json:"analysis_id"`
		AnswerID   int64    `json:"answer_id"`
		PointKeys  []string `json:"point_keys"`
	}) (any, error) {
		d, err := s.l.problemDetail(pid)
		if err != nil {
			return nil, err
		}
		row, a, _ := latestAnalysis(d)
		if a == nil || row.num("id") != b.AnalysisID {
			return nil, ledgerErr("the proposal has changed; refresh before linking an answer")
		}
		if len(b.PointKeys) == 0 {
			return nil, ledgerErr("choose a point for this answer")
		}
		points, err := answerRulingPoints(a, b.PointKeys)
		if err != nil {
			return nil, err
		}
		// Only an actual saved user answer in this problem can supply a ruling.
		for _, c := range ofKind(d, "context") {
			if c.num("id") != b.AnswerID || c.str("author") != "user" {
				continue
			}
			var m answerMeta
			if json.Unmarshal([]byte(c.str("meta")), &m) != nil || m.AnalysisID == 0 || m.QuestionIndex < 0 || strings.TrimSpace(m.Answer) == "" {
				break
			}
			if saved := savedAnswer(d, m.AnalysisID, m.QuestionIndex); saved == nil || saved.ContributionID != b.AnswerID {
				return nil, ledgerErr("this answer was replaced; choose its latest saved version")
			}
			id, err := s.l.saveAnswerRulings(d, b.AnalysisID, b.AnswerID, c.str("body"), nil, points)
			return idOf(id, err)
		}
		return nil, ledgerErr("choose a saved user answer from this problem")
	}))
}

// Point association is supplied explicitly by the user, never inferred from model text.
func answerRulingPoints(a *AnalysisOut, keys []string) ([]PointOut, error) {
	var points []PointOut
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			continue
		}
		i := slices.IndexFunc(a.Points, func(p PointOut) bool { return p.Key == key })
		if i < 0 {
			return nil, ledgerErr("unknown point %q; refresh before linking an answer", key)
		}
		seen[key] = true
		points = append(points, a.Points[i])
	}
	return points, nil
}

// Answer, explicit point rulings and audit events commit together. Existing answer text is immutable.
func (l *Ledger) saveAnswerRulings(d *Detail, analysisID, answerID int64, body string, meta D, points []PointOut) (int64, error) {
	pid := d.Problem.num("id")
	err := l.tx(func(x *tx) {
		var latest int64
		x.err = x.t.QueryRow("SELECT COALESCE(MAX(id),0) FROM contributions WHERE problem_id=? AND kind='analysis'", pid).Scan(&latest)
		if x.err != nil {
			return
		}
		if latest != analysisID {
			x.err = ledgerErr("the proposal has changed; refresh before saving")
			return
		}
		insert := func(text string, m D) int64 {
			raw, err := json.Marshal(m)
			if err != nil {
				x.err = err
				return 0
			}
			id := x.insert("contributions", Row{"problem_id": pid, "author": "user", "kind": "context", "body": text, "created_at": now(), "meta": string(raw)})
			x.event("user", "add_context", pid, D{"contribution_id": id})
			return id
		}
		if answerID == 0 {
			answerID = insert(body, meta)
		}
		ruled := rulings(d)
		for _, point := range points {
			text := fmt.Sprintf("%s: saved answer #%d — %s", point.Topic, answerID, body)
			if ruled[point.Key] == text {
				continue
			}
			insert(fmt.Sprintf("Ruling on [%s] %s", point.Key, text), D{"linked_answer_id": answerID, "point_key": point.Key})
		}
	})
	return answerID, err
}
