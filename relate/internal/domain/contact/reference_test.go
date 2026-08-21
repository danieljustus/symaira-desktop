package contact

import "testing"

func TestRefKindValid(t *testing.T) {
	if !RefKindPerson.Valid() || !RefKindOrganization.Valid() {
		t.Error("known reference kinds must be valid")
	}
	if RefKind("unknown").Valid() {
		t.Error("unknown reference kind is valid, want false")
	}
}
