package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CandidateMatch is one deterministic Memory entity candidate for a meeting
// participant label, with the reason it matched. ResolveCandidates never
// fuzzy-matches: every candidate is either an exact (case-insensitive) name
// match or a registered alias, so a reviewer always knows why an entity was
// suggested.
type CandidateMatch struct {
	Entity      MemoryEntity `json:"entity"`
	MatchReason string       `json:"match_reason"` // "exact_name" | "alias"
}

// ResolveCandidates finds Memory entities that deterministically match a
// participant label. Exact name matches are returned before alias matches.
func ResolveCandidates(label string) ([]CandidateMatch, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return nil, nil
	}
	entities, err := ListEntities()
	if err != nil {
		return nil, err
	}

	lower := strings.ToLower(label)
	var exact, aliasMatches []CandidateMatch
	for _, e := range entities {
		if strings.ToLower(e.Name) == lower {
			exact = append(exact, CandidateMatch{Entity: e, MatchReason: "exact_name"})
			continue
		}
		for _, alias := range e.Aliases {
			if strings.ToLower(alias) == lower {
				aliasMatches = append(aliasMatches, CandidateMatch{Entity: e, MatchReason: "alias"})
				break
			}
		}
	}
	return append(exact, aliasMatches...), nil
}

func runSymmemory(ctx context.Context, args []string) ([]byte, error) {
	bin, err := ResolveFunc(symmemoryName)
	if err != nil {
		return nil, fmt.Errorf("symmemory not found: %w", err)
	}
	out, stderr, err := runTool(ctx, bin, toolOpts{}, args...)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("symmemory %s timed out: %w", strings.Join(args, " "), ctx.Err())
		}
		return nil, fmt.Errorf("symmemory %s failed: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

// ShowEntity looks up one Memory entity by exact name. `entity show` prints
// human-readable relation/memory summaries after the JSON object even with
// --output json, so this decodes only the first JSON value and ignores the
// trailing text rather than treating it as a parse error.
func ShowEntity(name string) (*MemoryEntity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	data, err := runSymmemory(ctx, []string{"entity", "show", name, "--output", "json"})
	if err != nil {
		return nil, err
	}
	var entity MemoryEntity
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&entity); err != nil {
		return nil, fmt.Errorf("failed to unmarshal symmemory entity show output: %w", err)
	}
	return &entity, nil
}

// EnsureEntity returns the existing entity with this exact name, creating
// one of the given type if none exists. `entity add` fails on a duplicate
// name (unique constraint) rather than being idempotent, so a lookup always
// runs first; the add path is only taken when the lookup finds nothing.
func EnsureEntity(name, entityType string) (*MemoryEntity, error) {
	if existing, err := ShowEntity(name); err == nil {
		return existing, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := runSymmemory(ctx, []string{"entity", "add", name, "--type", entityType}); err != nil {
		// A concurrent caller may have created the same entity between the
		// lookup above and this call; only surface the add failure if the
		// entity still doesn't exist.
		if existing, showErr := ShowEntity(name); showErr == nil {
			return existing, nil
		}
		return nil, err
	}
	return ShowEntity(name)
}

// RelateEntities creates a directed relation between two entities by name.
// `entity relate` is idempotent (creating the same edge twice is a no-op),
// so callers do not need to check for an existing relation first.
func RelateEntities(from, relation, to string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := runSymmemory(ctx, []string{"entity", "relate", from, relation, to})
	return err
}

// MemoryRecord is one saved memory, as returned by `symmemory set --output json`.
type MemoryRecord struct {
	ID       string   `json:"id"`
	Content  string   `json:"content"`
	Scope    string   `json:"scope"`
	Entities []string `json:"entities"`
}

// SetMemory saves a new fact/context snippet, optionally linked to entities
// by name. Unlike `entity relate`, `symmemory set` is NOT idempotent: two
// calls with identical content each create a separate memory record with a
// distinct ID. Callers that must not duplicate a fact across repeated
// applies (see service.PublishMeetingProposal) are responsible for
// tracking which facts were already published and skipping them.
func SetMemory(value, scope string, entities []string) (*MemoryRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	args := []string{"set", "--value", value, "--scope", scope, "--output", "json"}
	if len(entities) > 0 {
		args = append(args, "--entities", strings.Join(entities, ","))
	}
	data, err := runSymmemory(ctx, args)
	if err != nil {
		return nil, err
	}
	var record MemoryRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("failed to unmarshal symmemory set output: %w", err)
	}
	return &record, nil
}
