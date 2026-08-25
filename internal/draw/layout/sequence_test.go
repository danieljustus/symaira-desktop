package layout

import (
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
)

// TestSequenceLayoutClosedForm verifies the deterministic column/row
// positioning for a two-actor, two-message sequence diagram.
func TestSequenceLayoutClosedForm(t *testing.T) {
	d := &ir.Diagram{
		Kind: ir.KindSequence,
		Custom: map[string]any{
			"actors": []map[string]any{
				{"ID": "Alice", "Label": "Alice"},
				{"ID": "Bob", "Label": "Bob"},
			},
			"messages": []map[string]any{
				{"From": "Alice", "To": "Bob", "Text": "Hello", "Order": 0},
				{"From": "Bob", "To": "Alice", "Text": "Hi", "Order": 1},
			},
		},
	}
	lay, err := SequenceLayoutFromIR(d)
	if err != nil {
		t.Fatal(err)
	}
	if lay.ActorX["Alice"] != 120 {
		t.Errorf("Alice x = %v, want 120", lay.ActorX["Alice"])
	}
	if lay.ActorX["Bob"] != 300 {
		t.Errorf("Bob x = %v, want 300", lay.ActorX["Bob"])
	}
	if len(lay.MessageY) != 2 {
		t.Fatalf("messageY = %d rows, want 2", len(lay.MessageY))
	}
	if lay.MessageY[1] <= lay.MessageY[0] {
		t.Errorf("message rows not ordered: %v", lay.MessageY)
	}
	if lay.Width <= 0 || lay.Height <= 0 {
		t.Errorf("layout bounds invalid: %v x %v", lay.Width, lay.Height)
	}
}

// TestSequenceLayoutRejectsNonSequence guards the layout API.
func TestSequenceLayoutRejectsNonSequence(t *testing.T) {
	d := &ir.Diagram{Kind: ir.KindGraph, Custom: map[string]any{}}
	if _, err := SequenceLayoutFromIR(d); err == nil {
		t.Error("expected error for non-sequence diagram")
	}
}
