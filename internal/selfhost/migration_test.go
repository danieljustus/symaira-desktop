package selfhost

import (
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/permissions"
)

// TestLegacyTokenAdminMigrationRunsOnce guards the one-time legacy-token
// admin migration against resurrection: after the migration ran once and an
// administrator later removes every user, a restart must NOT recreate the
// well-known admin account. The state store's migration marker (persisted
// via the abstracted storage backend, #311) is what makes this possible.
func TestLegacyTokenAdminMigrationRunsOnce(t *testing.T) {
	vaultRoot := t.TempDir()

	// First boot: no users exist, legacy token set → admin is created.
	first, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatalf("NewServer (first boot): %v", err)
	}
	users, err := first.perm.UserList()
	if err != nil {
		t.Fatalf("UserList: %v", err)
	}
	if len(users) != 1 || users[0].Name != "admin" {
		t.Fatalf("first boot users = %+v, want exactly [admin]", users)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The administrator deliberately removes every user.
	perm, err := permissions.NewManager(filepath.Join(vaultRoot, ".symdesk"))
	if err != nil {
		t.Fatalf("reopen permissions manager: %v", err)
	}
	if err := perm.UserRemove("admin"); err != nil {
		t.Fatalf("UserRemove: %v", err)
	}

	// Second boot: migration marker exists → admin must NOT come back.
	second, err := NewServer(ServerConfig{VaultRoot: vaultRoot, Token: testToken, Version: "test", Executable: "/bin/false"})
	if err != nil {
		t.Fatalf("NewServer (second boot): %v", err)
	}
	defer func() { _ = second.Close() }()
	users, err = second.perm.UserList()
	if err != nil {
		t.Fatalf("UserList (second boot): %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("legacy admin was resurrected after deliberate removal: %+v", users)
	}

	// The marker itself is persisted in the state store.
	migrated, err := second.state.Has(t.Context(), StateKeyLegacyAdminMigrated)
	if err != nil {
		t.Fatalf("state.Has: %v", err)
	}
	if !migrated {
		t.Fatal("migration marker missing from state store")
	}
}
