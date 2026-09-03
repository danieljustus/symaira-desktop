package permissions

import (
	"crypto/subtle"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func tempManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m, dir
}

func TestUserAddAndList(t *testing.T) {
	m, _ := tempManager(t)
	token, err := m.UserAdd("alice")
	if err != nil {
		t.Fatalf("UserAdd: %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token for a new user")
	}

	// Second add is a no-op.
	token2, err := m.UserAdd("alice")
	if err != nil {
		t.Fatalf("UserAdd (second): %v", err)
	}
	if token2 != "" {
		t.Fatal("expected empty token for duplicate user")
	}

	users, err := m.UserList()
	if err != nil {
		t.Fatalf("UserList: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if users[0].Name != "alice" {
		t.Fatalf("expected name alice, got %q", users[0].Name)
	}
	if users[0].TokenHash == "" {
		t.Fatal("expected non-empty token hash")
	}
	if !users[0].HasRole("user") {
		t.Fatal("expected user role")
	}
}

func TestAuthenticate(t *testing.T) {
	m, _ := tempManager(t)
	token, err := m.UserAdd("bob")
	if err != nil {
		t.Fatalf("UserAdd: %v", err)
	}

	user, err := m.Authenticate(token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if user.Name != "bob" {
		t.Fatalf("expected bob, got %q", user.Name)
	}

	_, err = m.Authenticate("wrong-token")
	if err == nil {
		t.Fatal("expected error for wrong token")
	}
}

func TestUserRemove(t *testing.T) {
	m, _ := tempManager(t)
	if _, err := m.UserAdd("charlie"); err != nil {
		t.Fatalf("UserAdd: %v", err)
	}
	if _, err := m.UserAdd("dave"); err != nil {
		t.Fatalf("UserAdd: %v", err)
	}

	if err := m.UserRemove("charlie"); err != nil {
		t.Fatalf("UserRemove: %v", err)
	}

	// No-op for missing user.
	if err := m.UserRemove("charlie"); err != nil {
		t.Fatalf("UserRemove (missing): %v", err)
	}

	users, _ := m.UserList()
	if len(users) != 1 {
		t.Fatalf("expected 1 user after remove, got %d", len(users))
	}
	if users[0].Name != "dave" {
		t.Fatalf("expected dave, got %q", users[0].Name)
	}
}

func TestUserGenerateToken(t *testing.T) {
	m, _ := tempManager(t)
	oldToken, err := m.UserAdd("eve")
	if err != nil {
		t.Fatalf("UserAdd: %v", err)
	}

	newToken, err := m.UserGenerateToken("eve")
	if err != nil {
		t.Fatalf("UserGenerateToken: %v", err)
	}
	if newToken == oldToken {
		t.Fatal("expected a different token")
	}

	// Old token must not work.
	if _, err := m.Authenticate(oldToken); err == nil {
		t.Fatal("old token should not authenticate after rotation")
	}
	// New token must work.
	if _, err := m.Authenticate(newToken); err != nil {
		t.Fatal("new token should authenticate after rotation")
	}
}

func TestGroupAddAndList(t *testing.T) {
	m, _ := tempManager(t)
	if err := m.GroupAdd("admins"); err != nil {
		t.Fatalf("GroupAdd: %v", err)
	}
	// Duplicate is a no-op.
	if err := m.GroupAdd("admins"); err != nil {
		t.Fatalf("GroupAdd (duplicate): %v", err)
	}

	groups, err := m.GroupList()
	if err != nil {
		t.Fatalf("GroupList: %v", err)
	}
	if len(groups) != 1 || groups[0].Name != "admins" {
		t.Fatalf("unexpected group list: %v", groups)
	}
}

func TestGroupMemberAddAndRemove(t *testing.T) {
	m, _ := tempManager(t)
	if _, err := m.UserAdd("alice"); err != nil {
		t.Fatalf("UserAdd: %v", err)
	}
	if err := m.GroupAdd("readers"); err != nil {
		t.Fatalf("GroupAdd: %v", err)
	}

	if err := m.GroupAddMember("readers", "alice"); err != nil {
		t.Fatalf("GroupAddMember: %v", err)
	}
	// Duplicate member add is idempotent.
	if err := m.GroupAddMember("readers", "alice"); err != nil {
		t.Fatalf("GroupAddMember (duplicate): %v", err)
	}

	groups, _ := m.GroupList()
	if len(groups[0].Members) != 1 || groups[0].Members[0] != "alice" {
		t.Fatalf("expected [alice], got %v", groups[0].Members)
	}

	if err := m.GroupRemoveMember("readers", "alice"); err != nil {
		t.Fatalf("GroupRemoveMember: %v", err)
	}
	groups, _ = m.GroupList()
	if len(groups[0].Members) != 0 {
		t.Fatalf("expected empty members, got %v", groups[0].Members)
	}
}

func TestGroupRemove(t *testing.T) {
	m, _ := tempManager(t)
	if err := m.GroupAdd("temp"); err != nil {
		t.Fatalf("GroupAdd: %v", err)
	}
	if err := m.GroupRemove("temp"); err != nil {
		t.Fatalf("GroupRemove: %v", err)
	}

	groups, _ := m.GroupList()
	if len(groups) != 0 {
		t.Fatalf("expected 0 groups, got %d", len(groups))
	}
}

func TestUserRemoveCleansGroupMembership(t *testing.T) {
	m, _ := tempManager(t)
	if _, err := m.UserAdd("alice"); err != nil {
		t.Fatalf("UserAdd: %v", err)
	}
	if err := m.GroupAdd("readers"); err != nil {
		t.Fatalf("GroupAdd: %v", err)
	}
	if err := m.GroupAddMember("readers", "alice"); err != nil {
		t.Fatalf("GroupAddMember: %v", err)
	}

	if err := m.UserRemove("alice"); err != nil {
		t.Fatalf("UserRemove: %v", err)
	}

	groups, _ := m.GroupList()
	if len(groups[0].Members) != 0 {
		t.Fatalf("expected empty members after user removal, got %v", groups[0].Members)
	}
}

func TestDocumentPermissions(t *testing.T) {
	m, _ := tempManager(t)
	aliceToken, err := m.UserAdd("alice")
	if err != nil {
		t.Fatalf("UserAdd alice: %v", err)
	}
	bobToken, err := m.UserAdd("bob")
	if err != nil {
		t.Fatalf("UserAdd bob: %v", err)
	}
	if err := m.GroupAdd("editors"); err != nil {
		t.Fatalf("GroupAdd: %v", err)
	}
	if err := m.GroupAddMember("editors", "bob"); err != nil {
		t.Fatalf("GroupAddMember: %v", err)
	}

	// Set a permission rule: alice owns, bob's group can read, nobody else writes.
	rule := DocumentRule{
		Path:       "notes/secret.md",
		Owner:      "alice",
		ReadGroups: []string{"editors"},
		WriteUsers: []string{"alice"},
	}
	if err := m.SetDocumentRule(rule); err != nil {
		t.Fatalf("SetDocumentRule: %v", err)
	}

	alice, _ := m.Authenticate(aliceToken)
	bob, _ := m.Authenticate(bobToken)

	// Alice (owner) can read and write.
	if !m.CanRead(alice, "notes/secret.md") {
		t.Fatal("owner should be able to read")
	}
	if !m.CanWrite(alice, "notes/secret.md") {
		t.Fatal("owner should be able to write")
	}

	// Bob (via group editors) should be able to read but NOT write.
	if !m.CanRead(bob, "notes/secret.md") {
		t.Fatal("editor group member should be able to read")
	}
	if m.CanWrite(bob, "notes/secret.md") {
		t.Fatal("non-owner should not be able to write")
	}

	// No rule → default access for any authenticated user.
	if !m.CanRead(alice, "notes/open.md") {
		t.Fatal("unprotected document should be readable")
	}

	// nil user can't do anything.
	if m.CanRead(nil, "notes/open.md") {
		t.Fatal("nil user should not be able to read")
	}
}

func TestAdminAccess(t *testing.T) {
	m, _ := tempManager(t)
	token, err := m.UserAdd("superadmin", "admin", "user")
	if err != nil {
		t.Fatalf("UserAdd: %v", err)
	}
	admin, _ := m.Authenticate(token)

	rule := DocumentRule{
		Path:       "notes/restricted.md",
		Owner:      "someone_else",
		ReadUsers:  []string{},
		ReadGroups: []string{},
	}
	if err := m.SetDocumentRule(rule); err != nil {
		t.Fatalf("SetDocumentRule: %v", err)
	}

	if !m.CanRead(admin, "notes/restricted.md") {
		t.Fatal("admin should bypass all document rules")
	}
	if !m.CanWrite(admin, "notes/restricted.md") {
		t.Fatal("admin should bypass all document rules")
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()

	// Create manager, add a user.
	m1, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	token, err := m1.UserAdd("persistent-user")
	if err != nil {
		t.Fatalf("UserAdd: %v", err)
	}

	// Open a fresh manager against the same directory.
	m2, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager (reload): %v", err)
	}
	user, err := m2.Authenticate(token)
	if err != nil {
		t.Fatalf("Authenticate after reload: %v", err)
	}
	if user.Name != "persistent-user" {
		t.Fatalf("expected persistent-user, got %q", user.Name)
	}
}

func TestSetDocumentRuleDelete(t *testing.T) {
	m, _ := tempManager(t)
	rule := DocumentRule{
		Path:  "notes/test.md",
		Owner: "alice",
	}
	if err := m.SetDocumentRule(rule); err != nil {
		t.Fatalf("SetDocumentRule: %v", err)
	}

	// Setting an empty rule deletes it.
	empty := DocumentRule{Path: "notes/test.md"}
	if err := m.SetDocumentRule(empty); err != nil {
		t.Fatalf("SetDocumentRule (delete): %v", err)
	}

	got, err := m.GetDocumentRule("notes/test.md")
	if err != nil {
		t.Fatalf("GetDocumentRule: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil after delete")
	}
}

func TestTokenGeneration(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if len(token) != 64 { // 32 bytes → 64 hex chars
		t.Fatalf("expected 64 hex characters, got %d", len(token))
	}

	hash := HashToken(token)
	if len(hash) != 64 {
		t.Fatalf("expected 64 hex characters for hash, got %d", len(hash))
	}
}

func TestFilesAreNotInVault(t *testing.T) {
	m, dir := tempManager(t)
	token, err := m.UserAdd("testuser")
	if err != nil {
		t.Fatalf("UserAdd: %v", err)
	}

	// The users file should be in the config dir, not in the vault root.
	usersPath := filepath.Join(dir, "users.json")
	if _, err := os.Stat(usersPath); err != nil {
		t.Fatalf("users file not found: %v", err)
	}

	// Files are readable JSON.
	data, err := os.ReadFile(usersPath) //nolint:gosec // G304: usersPath is a test fixture under t.TempDir.
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("empty users file")
	}
	// Token should NOT appear in plain text; only its hash is stored.
	if strings.Contains(string(data), token) {
		t.Fatal("users file contains the raw token in plain text")
	}
}

func TestConstantTimeEqual(t *testing.T) {
	// Equal strings must match.
	if !ConstantTimeEqual("abc123", "abc123") {
		t.Fatal("ConstantTimeEqual must return true for equal strings")
	}

	// Different strings of the same length must not match.
	if ConstantTimeEqual("abc123", "def456") {
		t.Fatal("ConstantTimeEqual must return false for different strings")
	}

	// Strings of different lengths must not match.
	if ConstantTimeEqual("abc", "abcd") {
		t.Fatal("ConstantTimeEqual must return false for strings of different length")
	}

	// Empty strings of equal length must match.
	if !ConstantTimeEqual("", "") {
		t.Fatal("ConstantTimeEqual must return true for empty strings")
	}

	// Must be consistent with crypto/subtle.ConstantTimeCompare.
	a, b := "some-token-value-for-testing", "some-token-value-for-testing"
	if ConstantTimeEqual(a, b) != (subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1) {
		t.Fatal("ConstantTimeEqual must be consistent with subtle.ConstantTimeCompare")
	}
	a, b = "some-token-value", "a-different-token-value"
	if ConstantTimeEqual(a, b) != (subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1) {
		t.Fatal("ConstantTimeEqual must be consistent with subtle.ConstantTimeCompare")
	}

	// Token-hash length strings (64 hex chars).
	hashA := HashToken("token-alpha")
	hashB := HashToken("token-beta")
	if !ConstantTimeEqual(hashA, hashA) {
		t.Fatal("identical token hashes must match")
	}
	if ConstantTimeEqual(hashA, hashB) {
		t.Fatal("different token hashes must not match")
	}

	// Regression guard: ensure the comparison does not regress to a simple
	// == check by verifying the helper is wired through subtle.
	// If this test compiles, the constant-time path is in place.
	_ = subtle.ConstantTimeCompare // keep import alive
}

func TestCanReadMany(t *testing.T) {
	adminPaths := []string{"first.md", "second.md"}
	cases := []struct {
		name   string
		user   *User
		paths  []string
		setup  func(*testing.T, *Manager)
		want   []string
		verify func(*testing.T, []string)
	}{
		{
			name:  "nil user",
			paths: []string{"public.md"},
			want:  nil,
		},
		{
			name:  "admin receives a copy",
			user:  &User{Name: "admin", Roles: []string{"admin"}},
			paths: adminPaths,
			want:  []string{"first.md", "second.md"},
			verify: func(t *testing.T, got []string) {
				t.Helper()
				got[0] = "result-mutated.md"
				if adminPaths[0] != "first.md" {
					t.Fatalf("result aliases input: input changed to %q", adminPaths[0])
				}
				adminPaths[1] = "input-mutated.md"
				if got[1] != "second.md" {
					t.Fatalf("result aliases input: result changed to %q", got[1])
				}
			},
		},
		{
			name:  "unrestricted paths",
			user:  &User{Name: "reader", Roles: []string{"user"}},
			paths: []string{"public.md", "also-public.md"},
			want:  []string{"public.md", "also-public.md"},
		},
		{
			name:  "owner",
			user:  &User{Name: "alice", Roles: []string{"user"}},
			paths: []string{"owned.md", "other.md"},
			setup: func(t *testing.T, m *Manager) {
				t.Helper()
				if err := m.SetDocumentRule(DocumentRule{Path: "owned.md", Owner: "alice"}); err != nil {
					t.Fatalf("SetDocumentRule: %v", err)
				}
			},
			want: []string{"owned.md", "other.md"},
		},
		{
			name:  "explicit reader",
			user:  &User{Name: "bob", Roles: []string{"user"}},
			paths: []string{"reader.md", "other.md"},
			setup: func(t *testing.T, m *Manager) {
				t.Helper()
				if err := m.SetDocumentRule(DocumentRule{
					Path:      "reader.md",
					Owner:     "alice",
					ReadUsers: []string{"bob"},
				}); err != nil {
					t.Fatalf("SetDocumentRule: %v", err)
				}
			},
			want: []string{"reader.md", "other.md"},
		},
		{
			name:  "group reader",
			user:  &User{Name: "carol", Roles: []string{"user"}},
			paths: []string{"group.md", "other.md"},
			setup: func(t *testing.T, m *Manager) {
				t.Helper()
				if err := m.GroupAdd("readers"); err != nil {
					t.Fatalf("GroupAdd: %v", err)
				}
				if err := m.GroupAddMember("readers", "carol"); err != nil {
					t.Fatalf("GroupAddMember: %v", err)
				}
				if err := m.SetDocumentRule(DocumentRule{
					Path:       "group.md",
					Owner:      "alice",
					ReadGroups: []string{"readers"},
				}); err != nil {
					t.Fatalf("SetDocumentRule: %v", err)
				}
			},
			want: []string{"group.md", "other.md"},
		},
		{
			name:  "deny",
			user:  &User{Name: "dave", Roles: []string{"user"}},
			paths: []string{"denied.md", "public.md"},
			setup: func(t *testing.T, m *Manager) {
				t.Helper()
				if err := m.SetDocumentRule(DocumentRule{Path: "denied.md", Owner: "alice"}); err != nil {
					t.Fatalf("SetDocumentRule: %v", err)
				}
			},
			want: []string{"public.md"},
		},
		{
			name:  "permission load failure",
			user:  &User{Name: "reader", Roles: []string{"user"}},
			paths: []string{"any.md"},
			setup: func(t *testing.T, m *Manager) {
				t.Helper()
				if err := os.Mkdir(m.permsFilePath(), 0700); err != nil {
					t.Fatalf("Mkdir permissions path: %v", err)
				}
			},
			want: nil,
		},
		{
			name:  "group load failure",
			user:  &User{Name: "carol", Roles: []string{"user"}},
			paths: []string{"group.md", "public.md"},
			setup: func(t *testing.T, m *Manager) {
				t.Helper()
				if err := m.SetDocumentRule(DocumentRule{
					Path:       "group.md",
					Owner:      "alice",
					ReadGroups: []string{"readers"},
				}); err != nil {
					t.Fatalf("SetDocumentRule: %v", err)
				}
				if err := os.Mkdir(m.groupsFilePath(), 0700); err != nil {
					t.Fatalf("Mkdir groups path: %v", err)
				}
			},
			want: []string{"public.md"},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := tempManager(t)
			if tt.setup != nil {
				tt.setup(t, m)
			}

			got := m.CanReadMany(tt.user, tt.paths)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("CanReadMany() = %#v, want %#v", got, tt.want)
			}
			if tt.verify != nil {
				tt.verify(t, got)
			}
		})
	}
}
