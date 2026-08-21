package event

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/danieljustus/symaira-room/internal/identity"
)

const (
	CurrentVersion = 1
	SigPrefix      = "ed25519:"
)

// Event kinds
const (
	KindRoomCreated        = "room.created"
	KindRoomRenamed        = "room.renamed"
	KindPolicyChanged      = "policy.changed"
	KindMemberAdded        = "member.added"
	KindMemberRemoved      = "member.removed"
	KindMemberRoleChanged  = "member.role_changed"
	KindNotePosted         = "note.posted"
	KindDecisionRecorded   = "decision.recorded"
	KindArtifactLinked     = "artifact.linked"
	KindArtifactUnlinked   = "artifact.unlinked"
	KindArtifactChanged    = "artifact.changed"
	KindRunRequested       = "run.requested"
	KindRunApproved        = "run.approved"
	KindRunDenied          = "run.denied"
	KindRunStarted         = "run.started"
	KindRunFinished        = "run.finished"
	KindRunFailed          = "run.failed"
	KindRunCancelled       = "run.cancelled"
	KindCheckpointReq      = "checkpoint.requested"
	KindCheckpointResolved = "checkpoint.resolved"
)

var KnownKinds = map[string]bool{
	KindRoomCreated:        true,
	KindRoomRenamed:        true,
	KindPolicyChanged:      true,
	KindMemberAdded:        true,
	KindMemberRemoved:      true,
	KindMemberRoleChanged:  true,
	KindNotePosted:         true,
	KindDecisionRecorded:   true,
	KindArtifactLinked:     true,
	KindArtifactUnlinked:   true,
	KindArtifactChanged:    true,
	KindRunRequested:       true,
	KindRunApproved:        true,
	KindRunDenied:          true,
	KindRunStarted:         true,
	KindRunFinished:        true,
	KindRunFailed:          true,
	KindRunCancelled:       true,
	KindCheckpointReq:      true,
	KindCheckpointResolved: true,
}

var (
	ErrInvalidSignature = errors.New("invalid event signature")
	ErrUnknownKind      = errors.New("unknown event kind")
)

type Event struct {
	V       int             `json:"v"`
	ID      string          `json:"id"`
	Room    string          `json:"room"`
	Author  string          `json:"author"`
	Seq     uint64          `json:"seq"`
	Prev    string          `json:"prev"`
	Lamport uint64          `json:"lamport"`
	TS      string          `json:"ts"`
	Kind    string          `json:"kind"`
	Body    json.RawMessage `json:"body"`
	Sig     string          `json:"sig,omitempty"`
}

func FormatTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func CanonicalBytes(e *Event) ([]byte, error) {
	// Build map to guarantee sorted keys in JSON encoding
	m := map[string]any{
		"v":       e.V,
		"id":      e.ID,
		"room":    e.Room,
		"author":  e.Author,
		"seq":     e.Seq,
		"prev":    e.Prev,
		"lamport": e.Lamport,
		"ts":      e.TS,
		"kind":    e.Kind,
		"body":    e.Body,
	}
	return json.Marshal(m)
}

func (e *Event) Sign(id *identity.Identity) error {
	if e.V == 0 {
		e.V = CurrentVersion
	}
	canonical, err := CanonicalBytes(e)
	if err != nil {
		return fmt.Errorf("canonical encoding: %w", err)
	}
	sigBytes := identity.Sign(id.PrivateKey, canonical)
	e.Sig = SigPrefix + base64.StdEncoding.EncodeToString(sigBytes)
	return nil
}

func (e *Event) VerifySignature(pubKey ed25519.PublicKey) error {
	if !strings.HasPrefix(e.Sig, SigPrefix) {
		return fmt.Errorf("%w: missing ed25519: prefix", ErrInvalidSignature)
	}
	rawSig := strings.TrimPrefix(e.Sig, SigPrefix)
	sigBytes, err := base64.StdEncoding.DecodeString(rawSig)
	if err != nil {
		return fmt.Errorf("%w: invalid base64 signature", ErrInvalidSignature)
	}
	canonical, err := CanonicalBytes(e)
	if err != nil {
		return fmt.Errorf("canonical encoding: %w", err)
	}
	if !identity.Verify(pubKey, canonical, sigBytes) {
		return ErrInvalidSignature
	}
	return nil
}

func (e *Event) MarshalJSONLine() ([]byte, error) {
	// Full event with Sig in canonical sorted order
	m := map[string]any{
		"v":       e.V,
		"id":      e.ID,
		"room":    e.Room,
		"author":  e.Author,
		"seq":     e.Seq,
		"prev":    e.Prev,
		"lamport": e.Lamport,
		"ts":      e.TS,
		"kind":    e.Kind,
		"body":    e.Body,
		"sig":     e.Sig,
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func UnmarshalJSONLine(data []byte) (*Event, error) {
	var e Event
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	return &e, nil
}
