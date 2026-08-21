package journal

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/members"
)

const (
	CodeSignatureInvalid        = "signature_invalid"
	CodeChainBroken             = "chain_broken"
	CodeSeqMismatch             = "seq_mismatch"
	CodeForkDetected            = "fork_detected"
	CodeAgentApprovalForbidden  = "agent_approval_forbidden"
	CodeUnknownAuthor           = "unknown_author"
	CodeUnauthorizedOwnerAction = "unauthorized_owner_action"
)

type Finding struct {
	Code     string   `json:"code"`
	Author   string   `json:"author,omitempty"`
	EventID  string   `json:"event_id,omitempty"`
	EventIDs []string `json:"event_ids,omitempty"`
	Message  string   `json:"message"`
}

type Report struct {
	Valid    bool      `json:"valid"`
	Findings []Finding `json:"findings"`
}

func (j *Journal) Verify() (*Report, error) {
	segments, err := j.ReadAllSegments()
	if err != nil {
		return nil, fmt.Errorf("read all segments: %w", err)
	}

	report := &Report{Valid: true, Findings: []Finding{}}
	state := members.NewState()

	// Track seen events per author to detect forks
	authorSeqs := make(map[string]map[uint64]*event.Event)

	// 1. Verify per-author segment hash chain and check for forks
	for author, events := range segments {
		if _, ok := authorSeqs[author]; !ok {
			authorSeqs[author] = make(map[uint64]*event.Event)
		}

		var prevHash = zeroHash
		var expectedSeq uint64 = 1

		segPath := j.SegmentPath(author)
		f, err := osOpen(segPath)
		if err != nil {
			continue
		}

		lines, err := readLines(f)
		_ = f.Close()
		if err != nil {
			report.Valid = false
			report.Findings = append(report.Findings, Finding{
				Code:    CodeChainBroken,
				Author:  author,
				Message: fmt.Sprintf("failed to read segment file for author %s: %v", author, err),
			})
			continue
		}

		for idx, lineBytes := range lines {
			if idx >= len(events) {
				break
			}
			ev := events[idx]

			// Check seq
			if ev.Seq != expectedSeq {
				report.Valid = false
				report.Findings = append(report.Findings, Finding{
					Code:    CodeSeqMismatch,
					Author:  author,
					EventID: ev.ID,
					Message: fmt.Sprintf("expected seq %d, got %d", expectedSeq, ev.Seq),
				})
			}

			// Check prev hash
			if ev.Prev != prevHash {
				report.Valid = false
				report.Findings = append(report.Findings, Finding{
					Code:    CodeChainBroken,
					Author:  author,
					EventID: ev.ID,
					Message: fmt.Sprintf("expected prev %s, got %s", prevHash, ev.Prev),
				})
			}

			// Fork detection (duplicate seq for same author with different event ID)
			if existing, exists := authorSeqs[author][ev.Seq]; exists {
				if existing.ID != ev.ID {
					report.Valid = false
					report.Findings = append(report.Findings, Finding{
						Code:     CodeForkDetected,
						Author:   author,
						EventIDs: []string{existing.ID, ev.ID},
						Message:  fmt.Sprintf("fork detected for author %s at seq %d: events %s and %s", author, ev.Seq, existing.ID, ev.ID),
					})
				}
			} else {
				authorSeqs[author][ev.Seq] = ev
			}

			prevHash = ComputeLineHash(lineBytes)
			expectedSeq++
		}
	}

	// 2. Verify overall total-ordered stream for signatures and membership state invariants
	mergedEvents := Merge(segments)

	for _, ev := range mergedEvents {
		// First event must be room.created or author must be known in membership state
		if ev.Kind == event.KindRoomCreated {
			if err := state.ApplyEvent(ev); err != nil {
				report.Valid = false
				report.Findings = append(report.Findings, Finding{
					Code:    CodeSignatureInvalid,
					EventID: ev.ID,
					Message: err.Error(),
				})
			}
			// Verify signature against body public_key
			member, ok := state.Members[ev.Author]
			if ok {
				if err := ev.VerifySignature(member.PublicKey); err != nil {
					report.Valid = false
					report.Findings = append(report.Findings, Finding{
						Code:    CodeSignatureInvalid,
						EventID: ev.ID,
						Message: fmt.Sprintf("signature verification failed: %v", err),
					})
				}
			}
			continue
		}

		member, known := state.Members[ev.Author]
		if !known {
			report.Valid = false
			report.Findings = append(report.Findings, Finding{
				Code:    CodeUnknownAuthor,
				Author:  ev.Author,
				EventID: ev.ID,
				Message: fmt.Sprintf("event signed by unknown author %s", ev.Author),
			})
			continue
		}

		// Verify signature against known member public key
		if err := ev.VerifySignature(member.PublicKey); err != nil {
			report.Valid = false
			report.Findings = append(report.Findings, Finding{
				Code:    CodeSignatureInvalid,
				Author:  ev.Author,
				EventID: ev.ID,
				Message: fmt.Sprintf("signature verification failed: %v", err),
			})
		}

		// Verify membership state transition and role invariants
		if err := state.ApplyEvent(ev); err != nil {
			report.Valid = false
			code := CodeUnauthorizedOwnerAction
			if err == members.ErrAgentApprovalForbidden {
				code = CodeAgentApprovalForbidden
			}
			report.Findings = append(report.Findings, Finding{
				Code:    code,
				Author:  ev.Author,
				EventID: ev.ID,
				Message: err.Error(),
			})
		}
	}

	return report, nil
}

// osOpen is a seam for tests to observe segment file reads (e.g. to prove
// appends no longer rescan segments). Production behavior is os.Open.
var osOpen = os.Open

type fileReadCloser interface {
	Read(p []byte) (n int, err error)
	Close() error
}

func readLines(f fileReadCloser) ([][]byte, error) {
	scanner := bufio.NewScanner(f)
	var lines [][]byte
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
	}
	return lines, scanner.Err()
}
