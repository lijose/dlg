package main

import (
	"encoding/json"
	"strings"
	"time"
)

// ApprovalChange describes a newer final proposal without changing the selection or approval.
type ApprovalChange struct {
	ApprovedAlternativeID int64 `json:"approved_alternative_id"`
	LatestAlternativeID   int64 `json:"latest_alternative_id"`
	TitleChanged          bool  `json:"title_changed"`
	DescriptionChanged    bool  `json:"description_changed"`
}

func approvalChange(d *Detail) *ApprovalChange {
	if d.Approval == nil || d.Selection == nil || d.Approval.num("selection_id") != d.Selection.num("id") || (d.Status != "approved" && d.Status != "sent") {
		return nil
	}
	var approved, latest Row
	for _, a := range d.Alternatives {
		if a.num("id") == d.Selection.num("alternative_id") {
			approved = a
		}
		if a.str("author") == "consensus" && (latest == nil || a.num("id") > latest.num("id")) {
			latest = a
		}
	}
	if approved == nil || latest == nil || approved.num("id") == latest.num("id") {
		return nil
	}
	// Audit order disambiguates operations in the same second, including imported ledgers.
	var approvedEvent, proposedEvent int64
	for _, e := range d.Events {
		var meta struct {
			ApprovalID    int64 `json:"approval_id"`
			AlternativeID int64 `json:"alternative_id"`
		}
		if json.Unmarshal([]byte(e.str("detail")), &meta) != nil {
			continue
		}
		if e.str("action") == "approve" && meta.ApprovalID == d.Approval.num("id") {
			approvedEvent = e.num("id")
		}
		if e.str("action") == "add_alternative" && meta.AlternativeID == latest.num("id") {
			proposedEvent = e.num("id")
		}
	}
	if approvedEvent != 0 && proposedEvent != 0 {
		if proposedEvent <= approvedEvent {
			return nil
		}
	} else {
		approvedAt, errA := time.Parse(time.RFC3339, d.Approval.str("created_at"))
		proposedAt, errP := time.Parse(time.RFC3339, latest.str("created_at"))
		if errA != nil || errP != nil || !proposedAt.After(approvedAt) {
			return nil
		}
	}
	clean := func(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")) }
	change := &ApprovalChange{ApprovedAlternativeID: approved.num("id"), LatestAlternativeID: latest.num("id"), TitleChanged: clean(approved.str("title")) != clean(latest.str("title")), DescriptionChanged: clean(approved.str("description")) != clean(latest.str("description"))}
	if !change.TitleChanged && !change.DescriptionChanged {
		return nil
	}
	return change
}
