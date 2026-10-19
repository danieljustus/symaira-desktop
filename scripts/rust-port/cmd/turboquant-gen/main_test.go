package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// TestGenerateMatchesCommittedFixture replays the full generator, including the
// pinned CoreKit module identity check, against the committed fixture.
func TestGenerateMatchesCommittedFixture(t *testing.T) {
	generated, err := generate()
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile("../../../../testdata/port/retrieval/turboquant-codec.json")
	if err != nil {
		t.Fatal(err)
	}
	var got, want fixture
	if err := json.Unmarshal(generated, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(committed, &want); err != nil {
		t.Fatal(err)
	}
	// The oracle commit identity is pinned by portgen, not by this test.
	got.Oracle.Commit, got.Oracle.Release = want.Oracle.Commit, want.Oracle.Release
	if !reflect.DeepEqual(got, want) {
		t.Fatal("generated fixture differs from committed fixture")
	}
	if len(got.Codecs) != 60 || len(got.Errors) != 21 {
		t.Fatalf("case counts codecs=%d errors=%d", len(got.Codecs), len(got.Errors))
	}
}
