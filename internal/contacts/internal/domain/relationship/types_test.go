package relationship

import "testing"

func TestInteractionKindValid(t *testing.T) {
	for _, kind := range []InteractionKind{
		InteractionNote,
		InteractionCall,
		InteractionEmail,
		InteractionMeeting,
		InteractionExternalReference,
	} {
		if !kind.Valid() {
			t.Errorf("InteractionKind(%q).Valid() = false, want true", kind)
		}
	}
	if InteractionKind("unknown").Valid() {
		t.Error("unknown interaction kind is valid, want false")
	}
}
