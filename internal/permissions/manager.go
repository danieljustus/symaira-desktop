package permissions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Manager orchestrates users, groups, and document-level permissions for a
// single vault. All persistent state lives under configDir (typically
// <vault>/.symdesk/).
type Manager struct {
	configDir string
	mu        sync.RWMutex
}

// NewManager returns a ready-to-use Manager. If configDir does not exist it
// is created automatically.
func NewManager(configDir string) (*Manager, error) {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return nil, fmt.Errorf("permissions: create config dir: %w", err)
	}
	return &Manager{configDir: configDir}, nil
}

// --- Authentication -----------------------------------------------------------

// Authenticate looks up a user by hashing the provided token and comparing it
// against every stored user's token hash. It returns the user on match, or an
// error when the token is not recognised.
func (m *Manager) Authenticate(token string) (*User, error) {
	if token == "" {
		return nil, fmt.Errorf("authentication required")
	}
	hash := sha256Hex(token)
	users, err := m.loadUsers()
	if err != nil {
		return nil, err
	}
	for i := range users {
		if ConstantTimeEqual(users[i].TokenHash, hash) {
			return &users[i], nil
		}
	}
	return nil, fmt.Errorf("invalid token")
}

// --- Document access checks ---------------------------------------------------

// CanRead reports whether a user may read the document at the given vault-
// relative path. Admins and document owners always have access.
func (m *Manager) CanRead(user *User, path string) bool {
	if user == nil {
		return false
	}
	if user.HasRole("admin") {
		return true
	}
	rules, _ := m.loadPermissions()
	rule := findRule(rules, path)
	// No rule → document is public (readable by authenticated users).
	if rule == nil {
		return true
	}
	if rule.Owner == user.Name {
		return true
	}
	if containsString(rule.ReadUsers, user.Name) {
		return true
	}
	groups, _ := m.loadGroups()
	for _, g := range rule.ReadGroups {
		if groupContains(groups, g, user.Name) {
			return true
		}
	}
	return false
}

// CanWrite reports whether a user may modify the document at path.
func (m *Manager) CanWrite(user *User, path string) bool {
	if user == nil {
		return false
	}
	if user.HasRole("admin") {
		return true
	}
	rules, _ := m.loadPermissions()
	rule := findRule(rules, path)
	// No rule → document is writable by authenticated users.
	if rule == nil {
		return true
	}
	if rule.Owner == user.Name {
		return true
	}
	if containsString(rule.WriteUsers, user.Name) {
		return true
	}
	groups, _ := m.loadGroups()
	for _, g := range rule.WriteGroups {
		if groupContains(groups, g, user.Name) {
			return true
		}
	}
	return false
}

// --- User management ----------------------------------------------------------

// UserAdd creates a new user and returns the generated plain-text token. The
// token is never stored; only its hash is persisted. If the user already
// exists the call is a no-op and an empty token is returned.
func (m *Manager) UserAdd(name string, roles ...string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("user name is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	users, err := m.loadUsers()
	if err != nil {
		return "", err
	}
	for _, u := range users {
		if u.Name == name {
			return "", nil // already exists
		}
	}
	if len(roles) == 0 {
		roles = []string{"user"}
	}
	token, err := GenerateToken()
	if err != nil {
		return "", err
	}
	users = append(users, User{Name: name, TokenHash: HashToken(token), Roles: roles})
	return token, m.saveUsers(users)
}

// UserList returns every registered user (token hashes are included; plain-
// text tokens are never stored).
func (m *Manager) UserList() ([]User, error) {
	return m.loadUsers()
}

// UserRemove deletes a user and all of their group memberships. It is a no-op
// when the user does not exist.
func (m *Manager) UserRemove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	users, err := m.loadUsers()
	if err != nil {
		return err
	}
	filtered := users[:0]
	found := false
	for _, u := range users {
		if u.Name == name {
			found = true
			continue
		}
		filtered = append(filtered, u)
	}
	if !found {
		return nil
	}
	if err := m.saveUsers(filtered); err != nil {
		return err
	}
	// Remove from all groups.
	groups, err := m.loadGroups()
	if err != nil {
		return err
	}
	dirty := false
	for i := range groups {
		members := groups[i].Members[:0]
		for _, m := range groups[i].Members {
			if m != name {
				members = append(members, m)
			}
		}
		if len(members) != len(groups[i].Members) {
			dirty = true
			groups[i].Members = members
		}
	}
	if dirty {
		return m.saveGroups(groups)
	}
	return nil
}

// UserGenerateToken replaces a user's token hash with a newly generated token
// and returns the plain-text token. Previous tokens stop working immediately.
func (m *Manager) UserGenerateToken(name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	users, err := m.loadUsers()
	if err != nil {
		return "", err
	}
	for i := range users {
		if users[i].Name == name {
			token, err := GenerateToken()
			if err != nil {
				return "", err
			}
			users[i].TokenHash = HashToken(token)
			return token, m.saveUsers(users)
		}
	}
	return "", fmt.Errorf("user %q not found", name)
}

// SetTokenHash directly overwrites a user's token hash. It is used during
// migration to seed the legacy admin token without re-generating it.
func (m *Manager) SetTokenHash(name, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	users, err := m.loadUsers()
	if err != nil {
		return err
	}
	for i := range users {
		if users[i].Name == name {
			users[i].TokenHash = hash
			return m.saveUsers(users)
		}
	}
	return fmt.Errorf("user %q not found", name)
}

// --- Group management ---------------------------------------------------------

// GroupAdd creates a new empty group. It is a no-op when the group already
// exists.
func (m *Manager) GroupAdd(name string) error {
	if name == "" {
		return fmt.Errorf("group name is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	groups, err := m.loadGroups()
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.Name == name {
			return nil
		}
	}
	groups = append(groups, Group{Name: name})
	return m.saveGroups(groups)
}

// GroupList returns all groups.
func (m *Manager) GroupList() ([]Group, error) {
	return m.loadGroups()
}

// GroupRemove deletes a group. It is a no-op when the group does not exist.
func (m *Manager) GroupRemove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	groups, err := m.loadGroups()
	if err != nil {
		return err
	}
	filtered := groups[:0]
	for _, g := range groups {
		if g.Name != name {
			filtered = append(filtered, g)
		}
	}
	return m.saveGroups(filtered)
}

// GroupAddMember adds a user to a group — idempotent.
func (m *Manager) GroupAddMember(groupName, userName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	groups, err := m.loadGroups()
	if err != nil {
		return err
	}
	for i := range groups {
		if groups[i].Name == groupName {
			if !containsString(groups[i].Members, userName) {
				groups[i].Members = append(groups[i].Members, userName)
				return m.saveGroups(groups)
			}
			return nil
		}
	}
	return fmt.Errorf("group %q not found", groupName)
}

// GroupRemoveMember removes a user from a group — idempotent.
func (m *Manager) GroupRemoveMember(groupName, userName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	groups, err := m.loadGroups()
	if err != nil {
		return err
	}
	for i := range groups {
		if groups[i].Name == groupName {
			members := groups[i].Members[:0]
			for _, m := range groups[i].Members {
				if m != userName {
					members = append(members, m)
				}
			}
			groups[i].Members = members
			return m.saveGroups(groups)
		}
	}
	return fmt.Errorf("group %q not found", groupName)
}

// --- Document permissions -----------------------------------------------------

// SetDocumentRule stores or replaces the permission rule for a vault-relative
// path. Passing an empty owner field deletes the rule.
func (m *Manager) SetDocumentRule(rule DocumentRule) error {
	if rule.Path == "" {
		return fmt.Errorf("path is required for a document rule")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rules, err := m.loadPermissions()
	if err != nil {
		return err
	}
	found := false
	for i := range rules {
		if rules[i].Path == rule.Path {
			if rule.Owner == "" && len(rule.ReadUsers) == 0 && len(rule.ReadGroups) == 0 &&
				len(rule.WriteUsers) == 0 && len(rule.WriteGroups) == 0 {
				// Delete the rule.
				rules = append(rules[:i], rules[i+1:]...)
			} else {
				rules[i] = rule
			}
			found = true
			break
		}
	}
	if !found && rule.Owner != "" {
		rules = append(rules, rule)
	}
	return m.savePermissions(rules)
}

// GetDocumentRule returns the permission rule for a path, or nil when none is
// set.
func (m *Manager) GetDocumentRule(path string) (*DocumentRule, error) {
	rules, err := m.loadPermissions()
	if err != nil {
		return nil, err
	}
	return findRule(rules, path), nil
}

// --- Persistence --------------------------------------------------------------

func (m *Manager) usersFilePath() string  { return filepath.Join(m.configDir, "users.json") }
func (m *Manager) groupsFilePath() string { return filepath.Join(m.configDir, "groups.json") }
func (m *Manager) permsFilePath() string  { return filepath.Join(m.configDir, "permissions.json") }

func (m *Manager) loadUsers() ([]User, error) {
	var users []User
	if err := readJSONFile(m.usersFilePath(), &users); err != nil {
		return nil, err
	}
	return users, nil
}

func (m *Manager) saveUsers(users []User) error {
	return writeJSONFile(m.usersFilePath(), users)
}

func (m *Manager) loadGroups() ([]Group, error) {
	var groups []Group
	if err := readJSONFile(m.groupsFilePath(), &groups); err != nil {
		return nil, err
	}
	return groups, nil
}

func (m *Manager) saveGroups(groups []Group) error {
	return writeJSONFile(m.groupsFilePath(), groups)
}

func (m *Manager) loadPermissions() ([]DocumentRule, error) {
	var rules []DocumentRule
	if err := readJSONFile(m.permsFilePath(), &rules); err != nil {
		return nil, err
	}
	return rules, nil
}

func (m *Manager) savePermissions(rules []DocumentRule) error {
	return writeJSONFile(m.permsFilePath(), rules)
}

// --- Helpers ------------------------------------------------------------------

func readJSONFile(path string, dest any) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // empty state is valid
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, dest)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func findRule(rules []DocumentRule, path string) *DocumentRule {
	// Exact match first, then wildcard.
	for i := range rules {
		if rules[i].Path == path {
			return &rules[i]
		}
	}
	for i := range rules {
		if rules[i].Path == "*" {
			return &rules[i]
		}
	}
	return nil
}

func containsString(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}

func groupContains(groups []Group, groupName, userName string) bool {
	for _, g := range groups {
		if g.Name == groupName {
			return containsString(g.Members, userName)
		}
	}
	return false
}
