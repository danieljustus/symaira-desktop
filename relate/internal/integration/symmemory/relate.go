package symmemory

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"time"
)

// RelateInput describes a directed relation to create in SymMemory. All
// mutation happens through the `symmemory entity relate` ID-based,
// provenance-aware path from symaira-memory#344.
type RelateInput struct {
	FromEntityID string
	ToEntityID   string
	RelationType string
	// Source is the caller-supplied source namespace, e.g. "symrelate".
	Source string
	// SourceRef is an opaque caller reference for idempotency, e.g. the
	// symrelate contact id that requested the relation.
	SourceRef string
	// Verification is either "verified" or "unverified".
	Verification string
	// EvidenceJSON is an optional, already-validated JSON blob.
	EvidenceJSON string
}

// Relation is the SymMemory response from a successful relate/unrelate call.
// It mirrors the EntityRelation JSON shape published by symaira-memory#344.
type Relation struct {
	ID           string    `json:"id"`
	FromEntityID string    `json:"from_entity_id"`
	ToEntityID   string    `json:"to_entity_id"`
	RelationType string    `json:"relation_type"`
	Source       string    `json:"source,omitempty"`
	SourceRef    string    `json:"source_ref,omitempty"`
	Verification string    `json:"verification,omitempty"`
	Evidence     string    `json:"evidence,omitempty"`
	CreatedBy    string    `json:"created_by,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func isValidEntityID(id string) bool {
	// SymMemory uses UUIDs; empty strings are never valid.
	return id != ""
}

// Relate runs `symmemory entity relate --from-id <from> --relation <rel>
// --to-id <to> ... --output json` and returns the stored relation. Any error
// (missing binary, timeout, non-zero exit, malformed JSON) collapses to
// ErrUnavailable so the optional integration degrades gracefully.
func Relate(ctx context.Context, in RelateInput) (*Relation, error) {
	if !isValidEntityID(in.FromEntityID) || !isValidEntityID(in.ToEntityID) || in.RelationType == "" {
		return nil, ErrUnavailable
	}

	binPath, err := exec.LookPath(binaryName)
	if err != nil {
		return nil, ErrUnavailable
	}

	args := []string{
		"entity", "relate",
		"--from-id", in.FromEntityID,
		"--relation", in.RelationType,
		"--to-id", in.ToEntityID,
		"--output", "json",
	}
	if in.Source != "" {
		args = append(args, "--source", in.Source)
	}
	if in.SourceRef != "" {
		args = append(args, "--source-ref", in.SourceRef)
	}
	if in.Verification != "" {
		args = append(args, "--verification", in.Verification)
	}
	if in.EvidenceJSON != "" {
		args = append(args, "--evidence-json", in.EvidenceJSON)
	}

	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, binPath, args...).Output()
	if err != nil {
		return nil, ErrUnavailable
	}

	dec := json.NewDecoder(bytes.NewReader(out))
	var rel Relation
	if err := dec.Decode(&rel); err != nil {
		return nil, ErrUnavailable
	}
	return &rel, nil
}

// Unrelate runs `symmemory entity unrelate --relation-id <id> --output json`
// and returns the removed relation. Errors collapse to ErrUnavailable.
func Unrelate(ctx context.Context, relationID string) (*Relation, error) {
	if relationID == "" {
		return nil, ErrUnavailable
	}

	binPath, err := exec.LookPath(binaryName)
	if err != nil {
		return nil, ErrUnavailable
	}

	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, binPath, "entity", "unrelate", "--relation-id", relationID, "--output", "json").Output()
	if err != nil {
		return nil, ErrUnavailable
	}

	dec := json.NewDecoder(bytes.NewReader(out))
	var rel Relation
	if err := dec.Decode(&rel); err != nil {
		return nil, ErrUnavailable
	}
	return &rel, nil
}
