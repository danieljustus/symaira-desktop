package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/internal/contacts"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

const noteContactProperty = "contact"

// ResolveContactReferencesByID returns every vault item carrying a reviewed
// contact reference. The lookup is identity-based: the cached display name in
// the note is not consulted, so renames and stale caches cannot hide links.
// A successful lookup refreshes the cache in each matching reference.
func (s *Service) ResolveContactReferencesByID(contactID string) (*ContactReferences, error) {
	contactID = strings.TrimSpace(contactID)
	if contactID == "" {
		return nil, fmt.Errorf("a contact id is required")
	}

	out := &ContactReferences{
		Name:           contactID,
		ContactID:      contactID,
		Refs:           []contacts.Ref{},
		Documents:      []DocsListResult{},
		Meetings:       []ContactMeetingRef{},
		StoreAvailable: contacts.Available(),
	}
	if !out.StoreAvailable {
		return out, s.collectContactReferences(out, contactID, nil)
	}

	ref, err := contacts.ResolveRef(contactID)
	if err != nil {
		return nil, err
	}
	out.Name = ref.DisplayName
	out.Refs = append(out.Refs, *ref)
	if err := s.refreshContactReferences(*ref); err != nil {
		return nil, err
	}
	return out, s.collectContactReferences(out, contactID, ref)
}

// LinkNoteContact writes the reference-only contact property on an ordinary
// note. It is an explicit operation: resolving a name or id never writes a
// contact reference, and this method never creates a contact in the store.
func (s *Service) LinkNoteContact(notePath, contactID string) (*contacts.Ref, error) {
	ref, err := s.resolveContactForWrite(contactID)
	if err != nil {
		return nil, err
	}
	doc, err := s.loadVaultDocument(notePath)
	if err != nil {
		return nil, err
	}

	value := map[string]interface{}{}
	if existing, ok := doc.Frontmatter[noteContactProperty].(map[string]interface{}); ok {
		for key, item := range existing {
			value[key] = item
		}
	}
	value["provider"] = ref.Provider
	value["schema_version"] = ref.SchemaVersion
	value["id"] = ref.ID
	value["kind"] = ref.Kind
	value["display_name"] = ref.DisplayName
	doc.Frontmatter[noteContactProperty] = value
	if err := s.writeVaultFrontmatter(notePath, doc); err != nil {
		return nil, err
	}
	return ref, nil
}

// UnlinkNoteContact removes only the ordinary note's contact property. It
// leaves all other frontmatter, body text, meeting participant data and the
// authoritative contact store untouched.
func (s *Service) UnlinkNoteContact(notePath string) error {
	doc, err := s.loadVaultDocument(notePath)
	if err != nil {
		return err
	}
	delete(doc.Frontmatter, noteContactProperty)
	return s.writeVaultFrontmatter(notePath, doc)
}

// BacklinksForContactID exposes the sidecar's contact edge query without
// making callers construct the internal target namespace themselves.
func (s *Service) BacklinksForContactID(contactID string) ([]string, error) {
	ref, err := s.resolveContactForWrite(contactID)
	if err != nil {
		return nil, err
	}
	links, err := s.DB.GetBacklinks(contacts.ReferenceTarget(*ref))
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(links))
	for _, path := range links {
		rel, relErr := filepath.Rel(s.VaultRoot, path)
		if relErr != nil {
			rel = path
		}
		result = append(result, rel)
	}
	sort.Strings(result)
	return result, nil
}

func (s *Service) resolveContactForWrite(contactID string) (*contacts.Ref, error) {
	if !contacts.Available() {
		return nil, ErrContactStoreUnavailable
	}
	return contacts.ResolveRef(strings.TrimSpace(contactID))
}

func (s *Service) loadVaultDocument(notePath string) (*vault.Document, error) {
	absPath, err := vault.SecurePath(s.VaultRoot, notePath)
	if err != nil {
		return nil, err
	}
	return vault.ParseFile(absPath)
}

func (s *Service) writeVaultFrontmatter(notePath string, doc *vault.Document) error {
	absPath, err := vault.SecurePath(s.VaultRoot, notePath)
	if err != nil {
		return err
	}
	frontmatter, err := yaml.Marshal(doc.Frontmatter)
	if err != nil {
		return fmt.Errorf("encode frontmatter: %w", err)
	}
	content := "---\n" + string(frontmatter) + "---\n" + doc.Body
	raw, err := os.ReadFile(absPath) //nolint:gosec // absPath was validated by vault.SecurePath
	if err != nil {
		return fmt.Errorf("read %s: %w", notePath, err)
	}
	hash := sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != doc.SHA256 {
		return fmt.Errorf("%s changed on disk since it was read; re-run", notePath)
	}
	s.snapshotBefore(absPath)
	if err := os.WriteFile(absPath, []byte(content), 0600); err != nil { //nolint:gosec // absPath was validated above
		return fmt.Errorf("write %s: %w", notePath, err)
	}
	newDoc, err := vault.ParseFile(absPath)
	if err != nil {
		return err
	}
	return s.IndexDocument(newDoc)
}

func (s *Service) refreshContactReferences(ref contacts.Ref) error {
	return vault.Walk(s.VaultRoot, func(path string) error {
		doc, err := vault.ParseFile(path)
		if err != nil {
			return nil
		}
		if !refreshContactMaps(doc.Frontmatter, ref) {
			return nil
		}
		rel, err := filepath.Rel(s.VaultRoot, path)
		if err != nil {
			return err
		}
		return s.writeVaultFrontmatter(rel, doc)
	})
}

func refreshContactMaps(value interface{}, ref contacts.Ref) bool {
	changed := false
	switch v := value.(type) {
	case map[string]interface{}:
		provider, providerOK := v["provider"].(string)
		kind, kindOK := v["kind"].(string)
		if id, ok := v["id"].(string); ok && id == ref.ID && providerOK && provider == ref.Provider && kindOK && kind == ref.Kind {
			if v["display_name"] != ref.DisplayName {
				v["display_name"] = ref.DisplayName
				changed = true
			}
			return changed
		}
		for _, child := range v {
			if refreshContactMaps(child, ref) {
				changed = true
			}
		}
	case []interface{}:
		for _, child := range v {
			if refreshContactMaps(child, ref) {
				changed = true
			}
		}
	}
	return changed
}

func (s *Service) collectContactReferences(out *ContactReferences, contactID string, identity *contacts.Ref) error {
	return vault.Walk(s.VaultRoot, func(path string) error {
		doc, err := vault.ParseFile(path)
		if err != nil {
			return nil
		}
		refs := contacts.ReferencesInFrontmatter(doc.Frontmatter)
		matched := false
		for _, candidate := range refs {
			matches := candidate.ID == contactID
			if identity != nil {
				matches = matches && candidate.Provider == identity.Provider && candidate.Kind == identity.Kind
			}
			if matches {
				matched = true
				break
			}
		}
		if !matched {
			return nil
		}
		rel, err := filepath.Rel(s.VaultRoot, path)
		if err != nil {
			return err
		}
		if doc.Type == "meeting" {
			_, fm, err := s.loadMeetingFrontmatter(rel)
			if err != nil {
				return nil
			}
			for _, participant := range fm.Participants {
				if participant.ContactRef != nil && participant.ContactRef.ID == contactID {
					out.Meetings = append(out.Meetings, ContactMeetingRef{
						Path: rel, Title: doc.Title, MeetingID: fm.MeetingID,
						StartedAt: fm.StartedAt, Participant: participant.Label,
					})
				}
			}
			return nil
		}
		out.Documents = append(out.Documents, DocsListResult{Path: rel, Title: doc.Title, Type: doc.Type})
		return nil
	})
}
