package run

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/journal"
	"github.com/danieljustus/symaira-room/internal/members"
)

var (
	ErrAgentCheckpointResolveForbidden = errors.New("agent identity is forbidden from resolving checkpoints")
	ErrCheckpointNotFound              = errors.New("checkpoint not found")
	ErrCheckpointAlreadyResolved       = errors.New("checkpoint already resolved")
)

type Checkpoint struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	Question  string `json:"question"`
	Answer    string `json:"answer,omitempty"`
	State     string `json:"state"` // "requested" or "resolved"
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func RequestCheckpoint(roomDir, runID, question string, id *identity.Identity) (*event.Event, error) {
	chkID := "chk_" + journal.ComputeLineHash([]byte(runID + question + time.Now().String()))[7:23]
	bodyMap := map[string]string{
		"checkpoint_id": chkID,
		"run_id":        runID,
		"question":      question,
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	j := journal.New(filepath.Join(roomDir, "journal"))
	ev := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_" + chkID[4:],
		Room:   "rm_test",
		Author: id.MemberID,
		Kind:   event.KindCheckpointReq,
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

func ResolveCheckpoint(roomDir, chkID, answer string, id *identity.Identity) (*event.Event, error) {
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
		return nil, ErrAgentCheckpointResolveForbidden
	}

	chks := ProjectCheckpoints(merged)
	chk, exists := chks[chkID]
	if !exists {
		return nil, ErrCheckpointNotFound
	}
	if chk.State == "resolved" {
		return nil, ErrCheckpointAlreadyResolved
	}

	bodyMap := map[string]string{
		"checkpoint_id": chkID,
		"answer":        answer,
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	ev := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_" + journal.ComputeLineHash([]byte(chkID + answer))[7:23],
		Room:   "rm_test",
		Author: id.MemberID,
		Kind:   event.KindCheckpointResolved,
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

func ProjectCheckpoints(events []*event.Event) map[string]*Checkpoint {
	chks := make(map[string]*Checkpoint)

	for _, ev := range events {
		switch ev.Kind {
		case event.KindCheckpointReq:
			var b struct {
				CheckpointID string `json:"checkpoint_id"`
				RunID        string `json:"run_id"`
				Question     string `json:"question"`
			}
			if err := json.Unmarshal(ev.Body, &b); err == nil && b.CheckpointID != "" {
				chks[b.CheckpointID] = &Checkpoint{
					ID:        b.CheckpointID,
					RunID:     b.RunID,
					Question:  b.Question,
					State:     "requested",
					Author:    ev.Author,
					CreatedAt: ev.TS,
					UpdatedAt: ev.TS,
				}
			}

		case event.KindCheckpointResolved:
			var b struct {
				CheckpointID string `json:"checkpoint_id"`
				Answer       string `json:"answer"`
			}
			if err := json.Unmarshal(ev.Body, &b); err == nil {
				if chk, exists := chks[b.CheckpointID]; exists {
					chk.State = "resolved"
					chk.Answer = b.Answer
					chk.UpdatedAt = ev.TS
				}
			}
		}
	}
	return chks
}

func WaitCheckpoint(ctx context.Context, roomDir, chkID string, pollInterval time.Duration) (*Checkpoint, error) {
	if pollInterval <= 0 {
		pollInterval = 500 * time.Millisecond
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		j := journal.New(filepath.Join(roomDir, "journal"))
		merged, err := j.MergeAll()
		if err == nil {
			chks := ProjectCheckpoints(merged)
			if chk, exists := chks[chkID]; exists && chk.State == "resolved" {
				return chk, nil
			}
		}

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, ErrWaitTimeout
			}
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
