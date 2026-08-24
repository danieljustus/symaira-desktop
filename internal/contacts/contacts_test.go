package contacts

import (
	"context"
	"testing"
)

func TestSeamOverrides(t *testing.T) {
	prevAvailable := AvailableFunc
	prevResolve := ResolveFunc
	prevFind := FindByNameFunc
	t.Cleanup(func() {
		AvailableFunc = prevAvailable
		ResolveFunc = prevResolve
		FindByNameFunc = prevFind
	})

	AvailableFunc = func(ctx context.Context) bool {
		return true
	}
	if !Available() {
		t.Error("expected Available() to return true when mocked")
	}

	ResolveFunc = func(ctx context.Context, id string) (*Ref, error) {
		if id == "c_123" {
			return &Ref{
				Provider:      Provider,
				SchemaVersion: SchemaVersion,
				ID:            "c_123",
				Kind:          "person",
				DisplayName:   "Ada Lovelace",
			}, nil
		}
		return nil, ErrContactNotFound
	}

	ref, err := ResolveRef("c_123")
	if err != nil {
		t.Fatalf("ResolveRef failed: %v", err)
	}
	if ref.ID != "c_123" || ref.DisplayName != "Ada Lovelace" || ref.Provider != Provider {
		t.Errorf("unexpected ref: %+v", ref)
	}

	_, err = ResolveRef("c_unknown")
	if err != ErrContactNotFound {
		t.Errorf("got %v, want ErrContactNotFound", err)
	}

	FindByNameFunc = func(ctx context.Context, name string) ([]Ref, error) {
		if name == "Ada" {
			return []Ref{{
				Provider:      Provider,
				SchemaVersion: SchemaVersion,
				ID:            "c_123",
				Kind:          "person",
				DisplayName:   "Ada Lovelace",
			}}, nil
		}
		return nil, nil
	}

	refs, err := FindRefsByName("Ada")
	if err != nil {
		t.Fatalf("FindRefsByName failed: %v", err)
	}
	if len(refs) != 1 || refs[0].ID != "c_123" {
		t.Errorf("unexpected refs: %+v", refs)
	}

	unmatched, err := FindRefsByName("Nobody")
	if err != nil {
		t.Fatalf("FindRefsByName failed: %v", err)
	}
	if len(unmatched) != 0 {
		t.Errorf("expected empty slice for unmatched name, got %+v", unmatched)
	}
}

func TestDefaultHelpersEmptyInputs(t *testing.T) {
	ctx := context.Background()

	// defaultResolve with empty contactID
	_, err := defaultResolve(ctx, "")
	if err == nil {
		t.Error("defaultResolve with empty string should error")
	}

	// defaultFindByName with blank name
	refs, err := defaultFindByName(ctx, "   ")
	if err != nil {
		t.Errorf("defaultFindByName blank name error = %v", err)
	}
	if refs != nil {
		t.Errorf("defaultFindByName blank name should return nil, got %+v", refs)
	}
}
