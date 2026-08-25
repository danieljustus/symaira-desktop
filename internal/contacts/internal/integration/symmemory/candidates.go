package symmemory

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
)

// Candidate is one scored, explainable entity match returned by SymMemory's
// `entity resolve` command. It intentionally mirrors the JSON shape published
// by symaira-memory#343 so callers can reason about the match without
// importing a Memory package.
type Candidate struct {
	EntityID    string   `json:"entity_id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Aliases     []string `json:"aliases"`
	Score       float64  `json:"score"`
	MatchKind   string   `json:"match_kind"`
	MatchReason string   `json:"match_reason"`
}

// CandidateOptions parameterizes the `entity resolve` call.
type CandidateOptions struct {
	// EntityType restricts results to this exact SymMemory entity type (e.g.
	// "person"). Empty means no restriction.
	EntityType string
	// Aliases are optional comparison hints that are never stored in SymMemory.
	AliasHints []string
	// Limit bounds the number of candidates returned. Zero means use the
	// SymMemory default (10 at the time this package was written).
	Limit int
}

// Candidates runs `symmemory entity resolve <query> --output json` with bounded
// timeout and returns the candidate list. All error conditions — missing binary,
// timeout, non-JSON output, malformed response — collapse to ErrUnavailable so
// callers can degrade gracefully, exactly like Discover and Context.
func Candidates(ctx context.Context, query string, opts CandidateOptions) ([]Candidate, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrUnavailable
	}

	binPath, err := exec.LookPath(binaryName)
	if err != nil {
		return nil, ErrUnavailable
	}

	args := []string{"entity", "resolve", query, "--output", "json"}
	if opts.EntityType != "" {
		args = append(args, "--type", opts.EntityType)
	}
	if len(opts.AliasHints) > 0 {
		args = append(args, "--aliases", strings.Join(opts.AliasHints, ","))
	}
	if opts.Limit > 0 {
		args = append(args, "--limit", strconv.Itoa(opts.Limit))
	}

	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, binPath, args...).Output()
	if err != nil {
		return nil, ErrUnavailable
	}

	dec := json.NewDecoder(bytes.NewReader(out))
	var candidates []Candidate
	if err := dec.Decode(&candidates); err != nil {
		return nil, ErrUnavailable
	}
	return candidates, nil
}
