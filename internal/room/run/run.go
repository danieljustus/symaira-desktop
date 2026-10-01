package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/internal/room/journal"
	"github.com/danieljustus/symaira-desktop/internal/room/members"
	roomconfig "github.com/danieljustus/symaira-desktop/internal/room/room"
)

var (
	ErrRunNotFound       = errors.New("run not found")
	ErrInvalidTransition = errors.New("invalid run state transition")
	ErrApprovalExpired   = errors.New("run approval has expired")
)

type State string

const (
	StateRequested State = "requested"
	StateApproved  State = "approved"
	StateDenied    State = "denied"
	StateStarted   State = "started"
	StateFinished  State = "finished"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

type Run struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	PlanFile    string        `json:"plan_file,omitempty"`
	Adapter     string        `json:"adapter,omitempty"`
	State       State         `json:"state"`
	Author      string        `json:"author"`
	CreatedAt   string        `json:"created_at"`
	UpdatedAt   string        `json:"updated_at"`
	ApprovalID  string        `json:"approval_id,omitempty"`
	Scope       string        `json:"scope,omitempty"`
	ExpiresAt   string        `json:"expires_at,omitempty"`
	Summary     string        `json:"summary,omitempty"`
	Error       string        `json:"error,omitempty"`
	Artifacts   []string      `json:"artifacts,omitempty"`
	Checkpoints []*Checkpoint `json:"checkpoints,omitempty"`
}

func Request(roomDir, title, planFile, adapter string, id *identity.Identity) (*event.Event, error) {
	roomID, err := readRoomID(roomDir)
	if err != nil {
		return nil, err
	}
	j := journal.New(filepath.Join(roomDir, "journal"))
	// compute unique run_id
	seq := 1
	if seg, err := j.ReadSegment(id.MemberID); err == nil {
		seq = len(seg) + 1
	}
	runID := "run_" + journal.ComputeLineHash([]byte(fmt.Sprintf("%s:%s:%d", id.MemberID, title, seq)))[7:23]

	bodyMap := map[string]string{
		"run_id":    runID,
		"title":     title,
		"plan_file": planFile,
		"adapter":   adapter,
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	ev := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_" + runID[4:],
		Room:   roomID,
		Author: id.MemberID,
		Kind:   event.KindRunRequested,
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

func Start(roomDir, runID string, id *identity.Identity) (*event.Event, error) {
	r, err := Get(roomDir, runID)
	if err != nil {
		return nil, err
	}
	if r.State != StateApproved {
		return nil, fmt.Errorf("%w: cannot start run in state '%s' (must be 'approved')", ErrInvalidTransition, r.State)
	}
	if r.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, r.ExpiresAt); err == nil && time.Now().After(t) {
			return nil, fmt.Errorf("%w: approval expired at %s", ErrApprovalExpired, r.ExpiresAt)
		}
	}

	bodyMap := map[string]string{
		"run_id": runID,
	}
	bodyBytes, _ := json.Marshal(bodyMap)
	roomID, err := readRoomID(roomDir)
	if err != nil {
		return nil, err
	}

	j := journal.New(filepath.Join(roomDir, "journal"))
	ev := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_" + journal.ComputeLineHash([]byte(runID + "start"))[7:23],
		Room:   roomID,
		Author: id.MemberID,
		Kind:   event.KindRunStarted,
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

func Cancel(roomDir, runID, reason string, id *identity.Identity) (*event.Event, error) {
	r, err := Get(roomDir, runID)
	if err != nil {
		return nil, err
	}
	if r.State == StateFinished || r.State == StateFailed || r.State == StateCancelled {
		return nil, fmt.Errorf("%w: cannot cancel run in terminal state '%s'", ErrInvalidTransition, r.State)
	}

	bodyMap := map[string]string{
		"run_id": runID,
		"reason": reason,
	}
	bodyBytes, _ := json.Marshal(bodyMap)
	roomID, err := readRoomID(roomDir)
	if err != nil {
		return nil, err
	}

	j := journal.New(filepath.Join(roomDir, "journal"))
	ev := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_" + journal.ComputeLineHash([]byte(runID + "cancel"))[7:23],
		Room:   roomID,
		Author: id.MemberID,
		Kind:   event.KindRunCancelled,
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

func Finish(roomDir, runID, summary string, artifacts []string, id *identity.Identity) (*event.Event, error) {
	r, err := Get(roomDir, runID)
	if err != nil {
		return nil, err
	}
	if r.State != StateStarted {
		return nil, fmt.Errorf("%w: cannot finish run in state '%s' (must be 'started')", ErrInvalidTransition, r.State)
	}

	bodyStruct := struct {
		RunID     string   `json:"run_id"`
		Summary   string   `json:"summary"`
		Artifacts []string `json:"artifacts,omitempty"`
	}{
		RunID:     runID,
		Summary:   summary,
		Artifacts: artifacts,
	}
	bodyBytes, _ := json.Marshal(bodyStruct)
	roomID, err := readRoomID(roomDir)
	if err != nil {
		return nil, err
	}

	j := journal.New(filepath.Join(roomDir, "journal"))
	ev := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_" + journal.ComputeLineHash([]byte(runID + "finish"))[7:23],
		Room:   roomID,
		Author: id.MemberID,
		Kind:   event.KindRunFinished,
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

func Fail(roomDir, runID, errMsg string, id *identity.Identity) (*event.Event, error) {
	r, err := Get(roomDir, runID)
	if err != nil {
		return nil, err
	}
	if r.State != StateStarted {
		return nil, fmt.Errorf("%w: cannot fail run in state '%s' (must be 'started')", ErrInvalidTransition, r.State)
	}

	bodyMap := map[string]string{
		"run_id": runID,
		"error":  errMsg,
	}
	bodyBytes, _ := json.Marshal(bodyMap)
	roomID, err := readRoomID(roomDir)
	if err != nil {
		return nil, err
	}

	j := journal.New(filepath.Join(roomDir, "journal"))
	ev := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_" + journal.ComputeLineHash([]byte(runID + "fail"))[7:23],
		Room:   roomID,
		Author: id.MemberID,
		Kind:   event.KindRunFailed,
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

// ProjectRuns projects a trusted single-room event stream. For room
// directories and mixed or untrusted journals, callers should use
// ProjectRunsInConfiguredRoom so both the room ID and root identity are bound
// to room.toml.
func ProjectRuns(events []*event.Event) map[string]*Run {
	return projectRuns(events, "", "", false)
}

func projectRuns(events []*event.Event, rootEvent, rootPubkey string, anchored bool) map[string]*Run {
	runs := make(map[string]*Run)
	membership := members.NewState()

	for _, ev := range events {
		switch ev.Kind {
		case event.KindRoomCreated, event.KindMemberAdded, event.KindMemberRemoved, event.KindMemberRoleChanged:
			// Membership changes also need the current signer's signature. Without
			// this check, a forged member.added event with Author set to an owner
			// could inject a key that later signs a projected approval.
			if anchored {
				_ = membership.ApplySignedEventWithRoot(ev, rootEvent, rootPubkey)
			} else {
				_ = applySignedMembershipEvent(membership, ev)
			}

		case event.KindRunRequested:
			var b struct {
				RunID    string `json:"run_id"`
				Title    string `json:"title"`
				PlanFile string `json:"plan_file"`
				Adapter  string `json:"adapter"`
			}
			if err := json.Unmarshal(ev.Body, &b); err == nil && b.RunID != "" {
				runs[b.RunID] = &Run{
					ID:        b.RunID,
					Title:     b.Title,
					PlanFile:  b.PlanFile,
					Adapter:   b.Adapter,
					State:     StateRequested,
					Author:    ev.Author,
					CreatedAt: ev.TS,
					UpdatedAt: ev.TS,
				}
			}

		case event.KindRunApproved:
			var b struct {
				RunID      string `json:"run_id"`
				ApprovalID string `json:"approval_id"`
				Scope      string `json:"scope"`
				ExpiresAt  string `json:"expires_at"`
			}
			// Only a current member allowed to approve, with a valid
			// signature, can approve; anything else leaves the run as is.
			// `symroom verify` reports such events as membership findings.
			if !approvalAuthorized(membership, ev) {
				continue
			}
			if err := json.Unmarshal(ev.Body, &b); err == nil {
				if r, exists := runs[b.RunID]; exists {
					r.State = StateApproved
					r.ApprovalID = b.ApprovalID
					r.Scope = b.Scope
					r.ExpiresAt = b.ExpiresAt
					r.UpdatedAt = ev.TS
				}
			}

		case event.KindRunDenied:
			var b struct {
				RunID  string `json:"run_id"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(ev.Body, &b); err == nil {
				if r, exists := runs[b.RunID]; exists {
					r.State = StateDenied
					r.Error = b.Reason
					r.UpdatedAt = ev.TS
				}
			}

		case event.KindRunStarted:
			var b struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(ev.Body, &b); err == nil {
				if r, exists := runs[b.RunID]; exists {
					r.State = StateStarted
					r.UpdatedAt = ev.TS
				}
			}

		case event.KindRunFinished:
			var b struct {
				RunID     string   `json:"run_id"`
				Summary   string   `json:"summary"`
				Artifacts []string `json:"artifacts"`
			}
			if err := json.Unmarshal(ev.Body, &b); err == nil {
				if r, exists := runs[b.RunID]; exists {
					r.State = StateFinished
					r.Summary = b.Summary
					r.Artifacts = b.Artifacts
					r.UpdatedAt = ev.TS
				}
			}

		case event.KindRunFailed:
			var b struct {
				RunID string `json:"run_id"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(ev.Body, &b); err == nil {
				if r, exists := runs[b.RunID]; exists {
					r.State = StateFailed
					r.Error = b.Error
					r.UpdatedAt = ev.TS
				}
			}

		case event.KindRunCancelled:
			var b struct {
				RunID  string `json:"run_id"`
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(ev.Body, &b); err == nil {
				if r, exists := runs[b.RunID]; exists {
					r.State = StateCancelled
					r.Error = b.Reason
					r.UpdatedAt = ev.TS
				}
			}
		}
	}

	chks := ProjectCheckpoints(events)
	for _, chk := range chks {
		if r, exists := runs[chk.RunID]; exists {
			r.Checkpoints = append(r.Checkpoints, chk)
		}
	}

	return runs
}

// ProjectRunsInRoom excludes events whose signed room field does not belong to
// the requested room. It assumes trusted root provenance; production room
// directories should use ProjectRunsInConfiguredRoom.
func ProjectRunsInRoom(events []*event.Event, roomID string) map[string]*Run {
	roomEvents := make([]*event.Event, 0, len(events))
	for _, ev := range events {
		if ev.Room == roomID {
			roomEvents = append(roomEvents, ev)
		}
	}
	return ProjectRuns(roomEvents)
}

// ProjectRunsInConfiguredRoom binds membership replay to the initialized
// room's root event and key as well as its ID. Use this for room directories
// and mixed or untrusted journals.
func ProjectRunsInConfiguredRoom(events []*event.Event, cfg *roomconfig.RoomConfig) map[string]*Run {
	roomEvents := make([]*event.Event, 0, len(events))
	for _, ev := range events {
		if ev.Room == cfg.ID {
			roomEvents = append(roomEvents, ev)
		}
	}
	return projectRuns(roomEvents, cfg.RootEvent, cfg.RootPubkey, true)
}

func List(roomDir string, pendingOnly bool) ([]*Run, error) {
	j := journal.New(filepath.Join(roomDir, "journal"))
	merged, err := j.MergeAll()
	if err != nil {
		return nil, err
	}

	cfg, err := roomconfig.ReadRoomConfig(roomDir)
	if err != nil {
		return nil, err
	}
	runsMap := ProjectRunsInConfiguredRoom(merged, cfg)
	var list []*Run
	for _, r := range runsMap {
		if pendingOnly && r.State != StateRequested && r.State != StateApproved {
			continue
		}
		list = append(list, r)
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt < list[j].CreatedAt
	})

	return list, nil
}

func Get(roomDir, runID string) (*Run, error) {
	j := journal.New(filepath.Join(roomDir, "journal"))
	merged, err := j.MergeAll()
	if err != nil {
		return nil, err
	}

	cfg, err := roomconfig.ReadRoomConfig(roomDir)
	if err != nil {
		return nil, err
	}
	runsMap := ProjectRunsInConfiguredRoom(merged, cfg)
	r, exists := runsMap[runID]
	if !exists {
		return nil, ErrRunNotFound
	}
	return r, nil
}

func readRoomID(roomDir string) (string, error) {
	cfg, err := roomconfig.ReadRoomConfig(roomDir)
	if err != nil {
		return "", err
	}
	return cfg.ID, nil
}

func approvalAuthorized(membership *members.State, ev *event.Event) bool {
	author, ok := membership.Members[ev.Author]
	if !ok || !author.CanPerform(members.ActionApprove) || ev.VerifySignature(author.PublicKey) != nil {
		return false
	}
	return membership.ApplyEvent(ev) == nil
}

func applySignedMembershipEvent(membership *members.State, ev *event.Event) bool {
	return membership.ApplySignedEvent(ev) == nil
}
