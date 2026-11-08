package main

import "testing"

func TestApprovalChangePreservesApprovedSelection(t *testing.T) {
	l := newLedger(t)
	pid, err := l.createProblem("Approval comparison", "", nil, "user")
	must(t, err)
	old, err := l.addAlternative(pid, "Original proposal", "Use in-memory tasks.", "consensus")
	must(t, err)
	_, err = l.selectAlternative(pid, &old, "Keep it simple")
	must(t, err)
	_, err = l.approve(pid, "Approved for implementation")
	must(t, err)
	d, err := l.problemDetail(pid)
	must(t, err)
	if d.ApprovalChange != nil {
		t.Fatal("warning before any new proposal")
	}
	next, err := l.addAlternative(pid, "Revised proposal", "Persist tasks to disk.", "consensus")
	must(t, err)
	d, err = l.problemDetail(pid)
	must(t, err)
	change := d.ApprovalChange
	if change == nil || change.ApprovedAlternativeID != old || change.LatestAlternativeID != next || !change.TitleChanged || !change.DescriptionChanged {
		t.Fatalf("missing changes: %+v", change)
	}
	if d.Selection.num("alternative_id") != old || d.Status != "approved" {
		t.Fatal("warning changed approval")
	}
	payload, err := l.implementationPayload(pid)
	must(t, err)
	if payload.Solution.Title != "Original proposal" || payload.Solution.Description != "Use in-memory tasks." {
		t.Fatalf("implementation silently changed: %+v", payload)
	}
	// Audit IDs establish ordering even when both operations have the same timestamp.
	for _, alt := range d.Alternatives {
		alt["created_at"] = "2026-11-08T00:00:00Z"
	}
	d.Approval["created_at"] = "2026-11-08T00:00:00Z"
	if approvalChange(d) == nil {
		t.Fatal("same-second proposal was missed")
	}
	d.Status = "sent"
	if approvalChange(d) == nil {
		t.Fatal("resend lost warning")
	}
	// Selecting the newer proposal makes the old approval stale; approval remains an explicit action.
	_, err = l.selectAlternative(pid, &next, "Use persistence instead")
	must(t, err)
	d, err = l.problemDetail(pid)
	must(t, err)
	if d.ApprovalChange != nil || d.Approval != nil || d.Status != "selected" {
		t.Fatal("stale approval retained")
	}
	_, err = l.approve(pid, "")
	must(t, err)
	d, err = l.problemDetail(pid)
	must(t, err)
	if d.ApprovalChange != nil {
		t.Fatal("new selection still shows an old warning")
	}
	// Identical regeneration (apart from outer whitespace/line endings) is not a new decision.
	_, err = l.addAlternative(pid, " Revised proposal\n", "Persist tasks to disk.\r\n", "consensus")
	must(t, err)
	d, err = l.problemDetail(pid)
	must(t, err)
	if d.ApprovalChange != nil {
		t.Fatal("identical proposal warned")
	}
	_, err = l.addAlternative(pid, "Manual option", "Unrelated option", "user")
	must(t, err)
	d, err = l.problemDetail(pid)
	must(t, err)
	if d.ApprovalChange != nil {
		t.Fatal("manual alternative treated as final proposal")
	}
}

func TestApprovalChangeOrderingAndContent(t *testing.T) {
	base := func() *Detail {
		return &Detail{
			Status: "approved", Selection: Row{"id": int64(3), "alternative_id": int64(1)}, Approval: Row{"id": int64(4), "selection_id": int64(3), "created_at": "2026-11-08T00:00:01Z"},
			Alternatives: []Row{{"id": int64(1), "title": "Old", "description": "Text", "author": "consensus", "created_at": "2026-11-08T00:00:00Z"}, {"id": int64(2), "title": "New", "description": "Text", "author": "consensus", "created_at": "2026-11-08T00:00:02Z"}},
		}
	}
	d := base()
	if c := approvalChange(d); c == nil || !c.TitleChanged || c.DescriptionChanged {
		t.Fatalf("title-only change: %+v", c)
	}
	d.Alternatives[1]["title"] = "Old"
	d.Alternatives[1]["description"] = "Revised text"
	if c := approvalChange(d); c == nil || c.TitleChanged || !c.DescriptionChanged {
		t.Fatalf("description-only change: %+v", c)
	}
	for _, timestamp := range []string{"2026-11-08T00:00:00Z", "2026-11-08T00:00:01Z", "invalid"} {
		d := base()
		d.Alternatives[1]["created_at"] = timestamp
		if approvalChange(d) != nil {
			t.Fatal("unproven order warned", timestamp)
		}
	}
	d = base()
	d.Events = []Row{{"id": int64(10), "action": "add_alternative", "detail": `{"alternative_id":2}`}, {"id": int64(11), "action": "approve", "detail": `{"approval_id":4}`}}
	if approvalChange(d) != nil {
		t.Fatal("ignored audit order")
	}
	d = base()
	d.Approval["selection_id"] = int64(99)
	if approvalChange(d) != nil {
		t.Fatal("mismatched approval used")
	}
	d = base()
	d.Status = "selected"
	if approvalChange(d) != nil {
		t.Fatal("unapproved selection warned")
	}
	d = base()
	d.Alternatives = append(d.Alternatives, Row{"id": int64(5), "author": "consensus", "title": "Old", "description": "Text", "created_at": "2026-11-08T00:00:03Z"})
	if approvalChange(d) != nil {
		t.Fatal("latest proposal matches approval; intermediate proposal should not warn")
	}
}
