package selfhost

import (
	"context"
	"errors"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/storage"
)

func openTestState(t *testing.T) *ServerState {
	t.Helper()
	state, err := OpenServerState(context.Background(), t.TempDir(), "")
	if err != nil {
		t.Fatalf("open server state: %v", err)
	}
	t.Cleanup(func() { _ = state.Close() })
	return state
}

func TestServerStateGetSetRemove(t *testing.T) {
	state := openTestState(t)
	ctx := context.Background()

	if _, err := state.Get(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
	}

	if err := state.Set(ctx, "server:port", []byte("8787")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := state.Get(ctx, "server:port")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "8787" {
		t.Fatalf("Get = %q, want %q", got, "8787")
	}

	if err := state.Remove(ctx, "server:port"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := state.Remove(ctx, "server:port"); err != nil {
		t.Fatalf("Remove absent must be idempotent: %v", err)
	}
	if _, err := state.Get(ctx, "server:port"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get after Remove = %v, want ErrNotFound", err)
	}
}

func TestServerStateJSONHelpers(t *testing.T) {
	state := openTestState(t)
	ctx := context.Background()

	type settings struct {
		AllowSignup bool   `json:"allow_signup"`
		Theme       string `json:"theme"`
	}
	if err := state.SetJSON(ctx, "settings", settings{AllowSignup: true, Theme: "dark"}); err != nil {
		t.Fatalf("SetJSON: %v", err)
	}

	var decoded settings
	if err := state.GetJSON(ctx, "settings", &decoded); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if !decoded.AllowSignup || decoded.Theme != "dark" {
		t.Fatalf("GetJSON decoded %+v, want {AllowSignup:true Theme:dark}", decoded)
	}

	var missing settings
	if err := state.GetJSON(ctx, "nope", &missing); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetJSON(missing) = %v, want ErrNotFound", err)
	}
}

func TestServerStateSetIfAbsent(t *testing.T) {
	state := openTestState(t)
	ctx := context.Background()

	wrote, err := state.SetIfAbsent(ctx, "migration:marker", []byte("once"))
	if err != nil {
		t.Fatalf("SetIfAbsent: %v", err)
	}
	if !wrote {
		t.Fatal("SetIfAbsent on empty key must write")
	}

	wrote, err = state.SetIfAbsent(ctx, "migration:marker", []byte("again"))
	if err != nil {
		t.Fatalf("SetIfAbsent second: %v", err)
	}
	if wrote {
		t.Fatal("SetIfAbsent must not overwrite an existing key")
	}

	got, _ := state.Get(ctx, "migration:marker")
	if string(got) != "once" {
		t.Fatalf("value after second SetIfAbsent = %q, want %q", got, "once")
	}

	exists, err := state.Has(ctx, "migration:marker")
	if err != nil || !exists {
		t.Fatalf("Has = %v/%v, want true/nil", exists, err)
	}
	exists, _ = state.Has(ctx, "absent")
	if exists {
		t.Fatal("Has(absent) = true, want false")
	}
}

func TestServerStateKeysAreNamespaced(t *testing.T) {
	// The backend must never see the bare key: the state store owns the
	// "state:" namespace so a shared storage database cannot collide.
	state := openTestState(t)
	ctx := context.Background()

	if err := state.Set(ctx, "session:abc", []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := state.backend.Get(ctx, "session:abc"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("bare key must not exist, got %v", err)
	}
	raw, err := state.backend.Get(ctx, stateKeyPrefix+"session:abc")
	if err != nil {
		t.Fatalf("namespaced key missing: %v", err)
	}
	if string(raw) != "1" {
		t.Fatalf("namespaced value = %q, want %q", raw, "1")
	}
}
