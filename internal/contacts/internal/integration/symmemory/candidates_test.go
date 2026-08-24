package symmemory

import (
	"context"
	"errors"
	"testing"
)

func TestCandidates_MissingBinary_ReturnsErrUnavailable(t *testing.T) {
	withEmptyPath(t)
	_, err := Candidates(context.Background(), "Alice", CandidateOptions{})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Candidates() error = %v, want ErrUnavailable", err)
	}
}

func TestCandidates_EmptyQuery_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `echo '[]'`)
	_, err := Candidates(context.Background(), "", CandidateOptions{})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Candidates(\"\") error = %v, want ErrUnavailable", err)
	}
}

func TestCandidates_ValidResponse_Succeeds(t *testing.T) {
	withFakeBinary(t, `echo '[{"entity_id":"e1","name":"Alice","type":"person","aliases":["Ali"],"score":1.0,"match_kind":"exact_name","match_reason":"exact name match"}]'`)
	candidates, err := Candidates(context.Background(), "Alice", CandidateOptions{})
	if err != nil {
		t.Fatalf("Candidates() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("Candidates() returned %d candidates, want 1", len(candidates))
	}
	c := candidates[0]
	if c.EntityID != "e1" || c.Name != "Alice" || c.Type != "person" || c.Score != 1.0 || c.MatchKind != "exact_name" {
		t.Errorf("Candidates()[0] = %+v, want exact_name match for Alice", c)
	}
}

func TestCandidates_NoMatches_ReturnsEmpty(t *testing.T) {
	withFakeBinary(t, `echo '[]'`)
	candidates, err := Candidates(context.Background(), "Zzz", CandidateOptions{})
	if err != nil {
		t.Fatalf("Candidates() error = %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("Candidates() = %v, want empty slice", candidates)
	}
}

func TestCandidates_PassesOptions(t *testing.T) {
	withFakeBinary(t, `echo '[{"entity_id":"e2","name":"SamProject","type":"project","score":0.9,"match_kind":"exact_name","match_reason":"exact name match"}]'`)
	candidates, err := Candidates(context.Background(), "Sam", CandidateOptions{
		EntityType: "project",
		AliasHints: []string{"Sammy"},
		Limit:      5,
	})
	if err != nil {
		t.Fatalf("Candidates() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].Type != "project" {
		t.Errorf("Candidates() = %+v, want one project candidate", candidates)
	}
}

func TestCandidates_MalformedJSON_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `echo 'not json'`)
	_, err := Candidates(context.Background(), "Alice", CandidateOptions{})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Candidates() error = %v, want ErrUnavailable", err)
	}
}

func TestCandidates_NonZeroExit_ReturnsErrUnavailable(t *testing.T) {
	withFakeBinary(t, `exit 1`)
	_, err := Candidates(context.Background(), "Alice", CandidateOptions{})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Candidates() error = %v, want ErrUnavailable", err)
	}
}

func TestCandidates_UnexpectedObject_ReturnsErrUnavailable(t *testing.T) {
	// The decoder expects a JSON array; an object should fail cleanly.
	withFakeBinary(t, `echo '{"entity_id":"e1"}'`)
	_, err := Candidates(context.Background(), "Alice", CandidateOptions{})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Candidates() error = %v, want ErrUnavailable", err)
	}
}
