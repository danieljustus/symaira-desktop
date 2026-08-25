package parse

import (
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
)

// TestParseSequenceDiagram verifies the sequenceDiagram subset parses into a
// KindSequence diagram with actors and messages in the Custom payload.
func TestParseSequenceDiagram(t *testing.T) {
	src := `sequenceDiagram
    participant Alice
    participant Bob
    Alice->>Bob: Hello Bob
    Bob-->>Alice: Hi Alice
`
	d, err := ParseMermaid(src)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != ir.KindSequence {
		t.Fatalf("kind = %q, want %q", d.Kind, ir.KindSequence)
	}
	actors, ok := d.Custom["actors"].([]SequenceActor)
	if !ok || len(actors) != 2 {
		t.Fatalf("actors = %#v, want 2", d.Custom["actors"])
	}
	if actors[0].ID != "Alice" || actors[0].Label != "Alice" {
		t.Errorf("actor[0] = %#v, want Alice", actors[0])
	}
	messages, ok := d.Custom["messages"].([]SequenceMessage)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v, want 2", d.Custom["messages"])
	}
	if messages[0].From != "Alice" || messages[0].To != "Bob" || messages[0].Text != "Hello Bob" {
		t.Errorf("message[0] = %#v, want Alice->Bob 'Hello Bob'", messages[0])
	}
}

// TestParseSequenceImplicitActors verifies actors mentioned only in message
// lines are created implicitly.
func TestParseSequenceImplicitActors(t *testing.T) {
	src := `sequenceDiagram
    Alice->>Bob: ping
`
	d, err := ParseMermaid(src)
	if err != nil {
		t.Fatal(err)
	}
	actors := d.Custom["actors"].([]SequenceActor)
	if len(actors) != 2 {
		t.Fatalf("actors = %d, want 2 (implicit)", len(actors))
	}
}

// TestParseSequenceNoParticipants rejects an empty sequence diagram.
func TestParseSequenceNoParticipants(t *testing.T) {
	_, err := ParseMermaid("sequenceDiagram\n")
	if err == nil {
		t.Fatal("expected error for sequence without participants")
	}
}

// TestParseSequenceBadLine rejects unsupported sequence syntax with a hint.
func TestParseSequenceBadLine(t *testing.T) {
	_, err := ParseMermaid("sequenceDiagram\nAlice Bob: nope\n")
	if err == nil {
		t.Fatal("expected error for malformed sequence line")
	}
}
