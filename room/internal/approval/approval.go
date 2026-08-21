package approval

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/danieljustus/symaira-room/internal/config"
	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/journal"
	"github.com/danieljustus/symaira-room/internal/members"
	"github.com/danieljustus/symaira-room/internal/run"
)

var (
	ErrAgentApprovalForbidden = errors.New("agent identity is forbidden from approving runs")
)

func Approve(roomDir, runID, scopeStr string, ttl time.Duration, id *identity.Identity) (*event.Event, error) {
	j := journal.New(filepath.Join(roomDir, "journal"))
	merged, err := j.MergeAll()
	if err != nil {
		return nil, err
	}
	state := members.NewState()
	for _, e := range merged {
		if err := state.ApplyEvent(e); err != nil {
			return nil, err
		}
	}
	m, exists := state.Members[id.MemberID]
	if exists && m.Role == members.RoleAgent {
		return nil, ErrAgentApprovalForbidden
	}

	r, err := run.Get(roomDir, runID)
	if err != nil {
		return nil, err
	}
	if r.State != run.StateRequested {
		return nil, fmt.Errorf("%w: cannot approve run in state '%s'", run.ErrInvalidTransition, r.State)
	}

	if scopeStr == "" {
		scopeStr = "all"
	}
	if ttl == 0 {
		cfg := config.LoadOrExit()
		if parsed, err := time.ParseDuration(cfg.Approval.DefaultTTL); err == nil {
			ttl = parsed
		} else {
			ttl = 30 * time.Minute
		}
	}

	expiresAt := time.Now().Add(ttl).Format(time.RFC3339)
	appID := "app_" + journal.ComputeLineHash([]byte(runID + scopeStr + expiresAt))[7:23]

	bodyStruct := struct {
		RunID      string `json:"run_id"`
		ApprovalID string `json:"approval_id"`
		Scope      string `json:"scope"`
		ExpiresAt  string `json:"expires_at"`
	}{
		RunID:      runID,
		ApprovalID: appID,
		Scope:      scopeStr,
		ExpiresAt:  expiresAt,
	}
	bodyBytes, _ := json.Marshal(bodyStruct)

	ev := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_" + appID[4:],
		Room:   "rm_test",
		Author: id.MemberID,
		Kind:   event.KindRunApproved,
		Body:   json.RawMessage(bodyBytes),
	}

	if err := j.PrepareEvent(ev); err != nil {
		return nil, err
	}
	if err := ev.Sign(id); err != nil {
		return nil, err
	}
	if err := j.Append(ev); err != nil {
		return nil, err
	}

	return ev, nil
}

func Deny(roomDir, runID, reason string, id *identity.Identity) (*event.Event, error) {
	r, err := run.Get(roomDir, runID)
	if err != nil {
		return nil, err
	}
	if r.State != run.StateRequested {
		return nil, fmt.Errorf("%w: cannot deny run in state '%s'", run.ErrInvalidTransition, r.State)
	}

	appID := "app_" + journal.ComputeLineHash([]byte(runID + reason))[7:23]
	bodyStruct := struct {
		RunID      string `json:"run_id"`
		ApprovalID string `json:"approval_id"`
		Reason     string `json:"reason"`
	}{
		RunID:      runID,
		ApprovalID: appID,
		Reason:     reason,
	}
	bodyBytes, _ := json.Marshal(bodyStruct)

	j := journal.New(filepath.Join(roomDir, "journal"))
	ev := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_" + appID[4:],
		Room:   "rm_test",
		Author: id.MemberID,
		Kind:   event.KindRunDenied,
		Body:   json.RawMessage(bodyBytes),
	}

	if err := j.PrepareEvent(ev); err != nil {
		return nil, err
	}
	if err := ev.Sign(id); err != nil {
		return nil, err
	}
	if err := j.Append(ev); err != nil {
		return nil, err
	}

	return ev, nil
}
