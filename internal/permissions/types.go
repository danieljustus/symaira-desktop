// Package permissions provides user accounts, group membership, and
// document-level access rules for the self-hosted server. Credentials are
// stored as files under the vault's .symdesk directory — not inside the
// Markdown vault — so they are never exposed through the document API.
package permissions

// User represents a named account with an individually revocable token.
// The token itself is never stored; only its SHA-256 hash is persisted.
type User struct {
	Name      string   `json:"name"`
	TokenHash string   `json:"token_hash"`
	Roles     []string `json:"roles"` // "admin", "user", "worker"
}

// HasRole reports whether the user carries the given role.
func (u *User) HasRole(role string) bool {
	for _, r := range u.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Group is a named set of users.
type Group struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// DocumentRule maps a vault-relative path to permission rules.
// The wildcard path "*" grants access to every document.
type DocumentRule struct {
	Path        string   `json:"path"`
	Owner       string   `json:"owner"`
	ReadUsers   []string `json:"read_users,omitempty"`
	ReadGroups  []string `json:"read_groups,omitempty"`
	WriteUsers  []string `json:"write_users,omitempty"`
	WriteGroups []string `json:"write_groups,omitempty"`
}
