package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

// TestPortNoteOperationContract is the Go-owned service-level harness for the
// note verbs of contract row VAULT-004: create, edit, move and delete. It
// records the observable filesystem outcome of the real Go implementations in
//
//	Service.NoteNew   — internal/service/service.go:551
//	Service.PropsEdit — internal/service/service.go:798
//	Service.NoteMove  — internal/service/service.go:772
//	Service.NoteDelete— internal/service/history.go:129 (trash via internal/history/trash.go:35)
//
// with byte-exact content, permission bits, content hashes, the resulting
// Markdown file set and the trash entry that a delete produces. Timestamps that
// the Go implementation takes from the wall clock are replaced by the literal
// placeholder {{CREATED}}/{{DELETED_AT}} so the document stays reproducible.
//
// Regenerate deliberately with:
//
//	PORT_GENERATE=1 go test -count=1 ./internal/service -run TestPortNoteOperationContract
func TestPortNoteOperationContract(t *testing.T) {
	fixture := buildNoteOperationFixture(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')

	path := filepath.Clean(noteOperationFixtureRel)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // fixture path is fixed relative to the repository
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v (run PORT_GENERATE=1 go test ./internal/service -run TestPortNoteOperationContract)", err)
	}
	normalizedCurrent := filterNotePlatform(current, runtime.GOOS)
	normalizedEncoded := filterNotePlatform(encoded, runtime.GOOS)
	if !bytes.Equal(normalizedCurrent, normalizedEncoded) {
		t.Fatalf("note operation fixture is stale; regenerate deliberately from the pinned Go oracle\n%s",
			caseDifference(normalizedEncoded, normalizedCurrent))
	}
}

const noteOperationFixtureRel = "../../testdata/port/vault/note-operations.json"

type noteOperationFixture struct {
	SchemaVersion int               `json:"schema_version"`
	Oracle        noteOracle        `json:"oracle"`
	SourceHashes  map[string]string `json:"source_hashes"`
	Cases         []noteCase        `json:"cases"`
}

type noteOracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type noteCase struct {
	ID      string `json:"id"`
	Verb    string `json:"verb"`
	Subject string `json:"subject"`
	// Setup lists the vault entries that exist before the verb runs.
	Setup []noteSetupEntry `json:"setup"`
	// Arguments are the sanitised verb arguments; the vault root is replaced by
	// the literal {{VAULT}}.
	Title     string    `json:"title,omitempty"`
	Content   string    `json:"content,omitempty"`
	From      string    `json:"from,omitempty"`
	To        string    `json:"to,omitempty"`
	Key       string    `json:"key,omitempty"`
	Value     string    `json:"value,omitempty"`
	VaultMode uint32    `json:"vault_mode"`
	Before    noteState `json:"before"`
	After     noteState `json:"after"`
	// ErrorWrapper is the Go error text up to and including the first ": ",
	// which is the part the Rust port must reproduce; the syscall text after it
	// is platform-specific and is covered by ErrorClass instead.
	ErrorWrapper string     `json:"error_wrapper"`
	ErrorClass   string     `json:"error_class"`
	Result       string     `json:"result,omitempty"`
	Trash        *noteTrash `json:"trash,omitempty"`
	Platform     string     `json:"platform"`
}

type noteSetupEntry struct {
	Path          string  `json:"path"`
	Kind          string  `json:"kind"`
	Mode          *uint32 `json:"mode,omitempty"`
	ContentBase64 string  `json:"content_base64,omitempty"`
}

type noteState struct {
	Markdown []noteFile `json:"markdown"`
}

type noteFile struct {
	Path    string  `json:"path"`
	Mode    *uint32 `json:"mode,omitempty"`
	Size    int64   `json:"size"`
	SHA256  string  `json:"sha256"`
	Content string  `json:"content,omitempty"`
}

type noteTrash struct {
	Name             string `json:"name"`
	OriginalPath     string `json:"original_path"`
	Size             int64  `json:"size"`
	ContentSHA256    string `json:"content_sha256"`
	MetadataBase64   string `json:"metadata_base64"`
	DeletedAtPresent bool   `json:"deleted_at_present"`
}

const (
	noteOperationOracleCommit  = "745c08e8144971c61133c5d0e5d61c7ce405aad2"
	noteOperationOracleRelease = "post-v0.12.2-security-880"
	createdPlaceholder         = "{{CREATED}}"
	deletedAtPlaceholder       = "{{DELETED_AT}}"
)

func buildNoteOperationFixture(t *testing.T) noteOperationFixture {
	t.Helper()
	cases := []noteCase{
		runNoteCase(t, noteCase{
			ID: "note-new-plain", Verb: "new", Subject: "Buy Milk & Eggs",
			Title: "Buy Milk & Eggs", Content: "First line\nSecond line",
		}),
		runNoteCase(t, noteCase{
			ID: "note-new-existing-file-keeps-mode", Verb: "new", Subject: "Kept Mode",
			Title: "Kept Mode", Content: "replacement body",
			Setup: []noteSetupEntry{{Path: "Kept_Mode.md", Kind: "file", Mode: modePointer(0o640), ContentBase64: base64.StdEncoding.EncodeToString([]byte("---\ntitle: older\n---\nolder body\n"))}},
		}),
		runNoteCase(t, noteCase{
			ID: "note-new-title-with-path-separator", Verb: "new", Subject: "nested/title",
			Title: "nested/title", Content: "body",
		}),
		runNoteCase(t, noteCase{
			ID: "note-new-read-only-vault", Verb: "new", Subject: "Locked", Platform: "unix",
			Title: "Locked", Content: "body", VaultMode: 0o500,
		}),
		runNoteCase(t, noteCase{
			ID: "props-edit-atomic-replace", Verb: "edit", Subject: "note.md",
			Key: "status", Value: "done",
			Setup: []noteSetupEntry{{Path: "note.md", Kind: "file", Mode: modePointer(0o640), ContentBase64: base64.StdEncoding.EncodeToString([]byte("---\ntitle: Editable\nstatus: open\n---\nbody\n"))}},
		}),
		runNoteCase(t, noteCase{
			ID: "props-edit-asn-rejected", Verb: "edit", Subject: "note.md",
			Key: "asn", Value: "7",
			Setup: []noteSetupEntry{{Path: "note.md", Kind: "file", Mode: modePointer(0o640), ContentBase64: base64.StdEncoding.EncodeToString([]byte("---\ntitle: Editable\n---\nbody\n"))}},
		}),
		runNoteCase(t, noteCase{
			ID: "props-edit-missing-file", Verb: "edit", Subject: "missing.md",
			Key: "status", Value: "done",
		}),
		runNoteCase(t, noteCase{
			ID: "note-move-same-directory", Verb: "move", Subject: "note.md",
			From: "note.md", To: "renamed.md",
			Setup: []noteSetupEntry{{Path: "note.md", Kind: "file", Mode: modePointer(0o640), ContentBase64: base64.StdEncoding.EncodeToString([]byte("---\ntitle: Movable\n---\nbody\n"))}},
		}),
		runNoteCase(t, noteCase{
			ID: "note-move-into-existing-directory", Verb: "move", Subject: "note.md",
			From: "note.md", To: "archive/note.md",
			Setup: []noteSetupEntry{
				{Path: "note.md", Kind: "file", Mode: modePointer(0o640), ContentBase64: base64.StdEncoding.EncodeToString([]byte("---\ntitle: Movable\n---\nbody\n"))},
				{Path: "archive", Kind: "directory", Mode: modePointer(0o750)},
			},
		}),
		runNoteCase(t, noteCase{
			ID: "note-move-missing-source", Verb: "move", Subject: "missing.md",
			From: "missing.md", To: "renamed.md",
		}),
		runNoteCase(t, noteCase{
			ID: "note-move-missing-target-parent", Verb: "move", Subject: "note.md",
			From: "note.md", To: "archive/note.md",
			Setup: []noteSetupEntry{{Path: "note.md", Kind: "file", Mode: modePointer(0o640), ContentBase64: base64.StdEncoding.EncodeToString([]byte("---\ntitle: Movable\n---\nbody\n"))}},
		}),
		runNoteCase(t, noteCase{
			ID: "note-move-onto-existing-file", Verb: "move", Subject: "note.md",
			From: "note.md", To: "occupied.md",
			Setup: []noteSetupEntry{
				{Path: "note.md", Kind: "file", Mode: modePointer(0o640), ContentBase64: base64.StdEncoding.EncodeToString([]byte("---\ntitle: Movable\n---\nmove me\n"))},
				{Path: "occupied.md", Kind: "file", Mode: modePointer(0o600), ContentBase64: base64.StdEncoding.EncodeToString([]byte("---\ntitle: Occupied\n---\nreplaced by the move\n"))},
			},
		}),
		runNoteCase(t, noteCase{
			ID: "note-move-target-is-directory", Verb: "move", Subject: "note.md",
			From: "note.md", To: "occupied",
			Setup: []noteSetupEntry{
				{Path: "note.md", Kind: "file", Mode: modePointer(0o640), ContentBase64: base64.StdEncoding.EncodeToString([]byte("---\ntitle: Movable\n---\nbody\n"))},
				{Path: "occupied", Kind: "directory", Mode: modePointer(0o750)},
			},
		}),
		runNoteCase(t, noteCase{
			ID: "note-delete-moves-to-trash", Verb: "delete", Subject: "folder/note.md",
			Setup: []noteSetupEntry{{Path: "folder/note.md", Kind: "file", Mode: modePointer(0o640), ContentBase64: base64.StdEncoding.EncodeToString([]byte("---\ntitle: Trashable\n---\nbody to preserve\n"))}},
		}),
		runNoteCase(t, noteCase{
			ID: "note-delete-missing-file", Verb: "delete", Subject: "missing.md",
		}),
		runNoteCase(t, noteCase{
			ID: "note-delete-directory-rejected", Verb: "delete", Subject: "folder",
			Setup: []noteSetupEntry{{Path: "folder", Kind: "directory", Mode: modePointer(0o750)}},
		}),
	}
	return noteOperationFixture{
		SchemaVersion: 1,
		Oracle:        noteOracle{Commit: noteOperationOracleCommit, Release: noteOperationOracleRelease},
		SourceHashes:  noteOperationSourceHashes(t),
		Cases:         cases,
	}
}

func noteOperationSourceHashes(t *testing.T) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	for _, rel := range []string{"internal/service/service.go", "internal/service/history.go", "internal/history/trash.go"} {
		//nolint:gosec // fixed repository-relative source paths
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		hashes[rel] = hex.EncodeToString(sum[:])
	}
	return hashes
}

func modePointer(mode uint32) *uint32 {
	value := mode
	return &value
}

// newIsolatedVault builds a temp vault root with a sidecar database outside the
// vault, so the recorded file set contains only vault content.
func newIsolatedVault(t *testing.T) (string, *sidecar.DB) {
	t.Helper()
	root := t.TempDir()
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	}
	dbPath := filepath.Join(t.TempDir(), "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return root, db
}

func runNoteCase(t *testing.T, spec noteCase) noteCase {
	t.Helper()
	if spec.Platform == "" {
		spec.Platform = "any"
	}
	if spec.VaultMode == 0 {
		spec.VaultMode = 0o750
	}
	root, db := newIsolatedVault(t)
	for _, entry := range spec.Setup {
		path := filepath.Join(root, filepath.FromSlash(entry.Path))
		switch entry.Kind {
		case "directory":
			if err := os.MkdirAll(path, 0o750); err != nil {
				t.Fatal(err)
			}
		default:
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			content, err := base64.StdEncoding.DecodeString(entry.ContentBase64)
			if err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0o600)
			if entry.Mode != nil {
				mode = os.FileMode(*entry.Mode)
			}
			if err := os.WriteFile(path, content, mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Chmod(root, os.FileMode(spec.VaultMode)); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // 0o500 is the read-only vault case under test
	t.Cleanup(func() { _ = os.Chmod(root, 0o750) })

	svc := New(root, db)
	spec.Before = noteStateOf(t, root)
	spec.Result = ""
	var err error
	var trashEntry *noteTrash
	switch spec.Verb {
	case "new":
		var relPath string
		relPath, err = svc.NoteNew(spec.Title, spec.Content, "")
		spec.Result = relPath
	case "edit":
		err = svc.PropsEdit(spec.Subject, spec.Key, spec.Value)
	case "move":
		err = svc.NoteMove(spec.From, spec.To)
	case "delete":
		trashed, trashErr := svc.NoteDelete(spec.Subject)
		err = trashErr
		if trashErr == nil && trashed != nil {
			spec.Result = trashed.Name
			trashEntry = noteTrashOf(t, root, trashed.Name, trashed.OriginalPath, trashed.Size)
		}
	default:
		t.Fatalf("unknown verb %q", spec.Verb)
	}
	if err == nil {
		spec.ErrorWrapper = ""
		spec.ErrorClass = ""
	} else {
		spec.ErrorWrapper = noteErrorWrapper(sanitiseNoteError(err.Error(), root))
		spec.ErrorClass = noteErrorClass(err)
	}
	spec.Trash = trashEntry
	spec.After = noteStateOf(t, root)
	return spec
}

// filterNotePlatform clears permission bits for the drift check on Windows,
// where the vault does not report Unix modes.
func filterNotePlatform(document []byte, goos string) []byte {
	if goos != "windows" {
		return document
	}
	var value noteOperationFixture
	if err := json.Unmarshal(document, &value); err != nil {
		return document
	}
	kept := make([]noteCase, 0, len(value.Cases))
	for _, item := range value.Cases {
		if item.Platform == "unix" {
			continue
		}
		for _, state := range []*noteState{&item.Before, &item.After} {
			for record := range state.Markdown {
				state.Markdown[record].Mode = nil
			}
		}
		for entry := range item.Setup {
			item.Setup[entry].Mode = nil
		}
		kept = append(kept, item)
	}
	value.Cases = kept
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return document
	}
	return append(encoded, '\n')
}

// caseDifference reports the first differing case and field so a drift is
// attributable; a raw line diff misleads as soon as one side omits a case.
func caseDifference(want, got []byte) string {
	var wantDoc, gotDoc noteOperationFixture
	if err := json.Unmarshal(want, &wantDoc); err != nil {
		return err.Error()
	}
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		return err.Error()
	}
	gotByID := make(map[string]string, len(gotDoc.Cases))
	for _, item := range gotDoc.Cases {
		encoded, err := json.Marshal(item)
		if err != nil {
			return err.Error()
		}
		gotByID[item.ID] = string(encoded)
	}
	for _, item := range wantDoc.Cases {
		encoded, err := json.Marshal(item)
		if err != nil {
			return err.Error()
		}
		counterpart, ok := gotByID[item.ID]
		if !ok {
			return fmt.Sprintf("fixture case %q is missing from the generated document", item.ID)
		}
		if counterpart != string(encoded) {
			return fmt.Sprintf("case %q differs:\n  fixture:   %s\n  generated: %s", item.ID, string(encoded), counterpart)
		}
		delete(gotByID, item.ID)
	}
	if len(gotByID) > 0 {
		for id := range gotByID {
			return fmt.Sprintf("generated case %q is missing from the fixture", id)
		}
	}
	return "cases are equal; only the surrounding document differs"
}

// firstDifference reports the first differing line pair so a drift is
// diagnosable without hand-diffing two large JSON documents.
func firstDifference(current, generated []byte) string {
	left := bytes.Split(current, []byte("\n"))
	right := bytes.Split(generated, []byte("\n"))
	for index := 0; index < len(left) && index < len(right); index++ {
		if !bytes.Equal(left[index], right[index]) {
			return fmt.Sprintf("line %d:\n  fixture:   %s\n  generated: %s", index+1, left[index], right[index])
		}
	}
	return fmt.Sprintf("length mismatch: fixture %d lines, generated %d lines", len(left), len(right))
}

func noteStateOf(t *testing.T, root string) noteState {
	t.Helper()
	state := noteState{Markdown: []noteFile{}}
	//nolint:gosec // root is a private test vault
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !strings.HasSuffix(relative, ".md") {
			return nil
		}
		//nolint:gosec // path is below the private test vault
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// The wall-clock creation stamp is normalised before the digest is
		// taken, so both the recorded bytes and their hash are reproducible.
		recorded := normaliseCreated(data)
		sum := sha256.Sum256(recorded)
		record := noteFile{
			Path:   relative,
			Size:   int64(len(data)),
			SHA256: hex.EncodeToString(sum[:]),
		}
		if len(data) <= 4096 {
			record.Content = string(recorded)
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			record.Mode = modePointer(uint32(info.Mode().Perm()))
		}
		state.Markdown = append(state.Markdown, record)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(state.Markdown, func(i, j int) bool { return state.Markdown[i].Path < state.Markdown[j].Path })
	return state
}

func noteTrashOf(t *testing.T, root, name, originalPath string, size int64) *noteTrash {
	t.Helper()
	trashDir := filepath.Join(root, ".symdesk", "trash")
	//nolint:gosec // trash directory lives inside the private test vault
	content, err := os.ReadFile(filepath.Join(trashDir, name))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	//nolint:gosec // metadata file is written by the Go trash implementation
	metadata, err := os.ReadFile(filepath.Join(trashDir, name+".trashinfo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Name         string `json:"name"`
		OriginalPath string `json:"original_path"`
		DeletedAt    string `json:"deleted_at"`
		Size         int64  `json:"size"`
	}
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Name != name || decoded.OriginalPath != originalPath || decoded.Size != size {
		t.Fatalf("trash metadata mismatch: %+v", decoded)
	}
	normalised := strings.ReplaceAll(string(metadata), decoded.DeletedAt, deletedAtPlaceholder)
	return &noteTrash{
		Name:             name,
		OriginalPath:     decoded.OriginalPath,
		Size:             decoded.Size,
		ContentSHA256:    hex.EncodeToString(sum[:]),
		MetadataBase64:   base64.StdEncoding.EncodeToString([]byte(normalised)),
		DeletedAtPresent: decoded.DeletedAt != "",
	}
}

// normaliseCreated replaces the wall-clock creation stamp with a placeholder so
// the fixture is reproducible; the surrounding byte sequence is untouched.
func normaliseCreated(data []byte) []byte {
	return replaceQuotedTimestamp(data, []byte(`created: "`), []byte(createdPlaceholder))
}

// replaceQuotedTimestamp rewrites the value of a quoted frontmatter key.
func replaceQuotedTimestamp(data, prefix, placeholder []byte) []byte {
	index := bytes.Index(data, prefix)
	if index < 0 {
		return data
	}
	start := index + len(prefix)
	end := bytes.IndexByte(data[start:], '"')
	if end < 0 {
		return data
	}
	out := make([]byte, 0, len(data))
	out = append(out, data[:start]...)
	out = append(out, placeholder...)
	out = append(out, data[start+end:]...)
	return out
}

// sanitiseNoteError removes the machine-specific vault root from Go error text
// and normalises embedded timestamps.
func sanitiseNoteError(message, root string) string {
	message = strings.ReplaceAll(message, root, "{{VAULT}}")
	if index := strings.Index(message, `created: "`); index >= 0 {
		start := index + len(`created: "`)
		if end := strings.IndexByte(message[start:], '"'); end >= 0 {
			message = message[:start] + createdPlaceholder + message[start+end:]
		}
	}
	return message
}

// stableErrorWrappers are the error prefixes the Go implementation itself
// constructs. Error text produced by the operating-system layer below them
// (for example the `statat <path>: ` prefix of an os.Root stat) is deliberately
// not recorded: it is platform-specific and carries no port contract.
var stableErrorWrappers = []string{
	"failed to write file: ",
	"failed to move file: ",
	"read file: ",
	"create temp file: ",
	"rename temp file: ",
	"cannot trash a directory: ",
	asnGuardMessage,
}

// asnGuardMessage is the exact Go guard text for a generic property edit of the
// reserved asn key (internal/service/service.go:800).
const asnGuardMessage = `use "symdesk doc asn <file> <next|N>" to assign an ASN safely`

// noteErrorWrapper keeps the deterministic part of a Go error message.
func noteErrorWrapper(message string) string {
	if message == "" {
		return ""
	}
	for _, wrapper := range stableErrorWrappers {
		if strings.HasPrefix(message, wrapper) {
			return wrapper
		}
	}
	return ""
}

// noteErrorClass classifies the failure by its Go wrapper and by the wrapped
// sentinel, so the fixture stays cross-platform.
func noteErrorClass(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "failed to write file: "):
		return "write_failed"
	case strings.HasPrefix(message, "failed to move file: "):
		return "move_failed"
	case strings.HasPrefix(message, "create temp file: "):
		return "create_temp"
	case strings.HasPrefix(message, "rename temp file: "):
		return "rename_temp"
	case strings.HasPrefix(message, "invalid vault-relative path:"):
		return "invalid_path"
	case strings.HasPrefix(message, "cannot trash a directory: "):
		return "trash_directory"
	case strings.HasPrefix(message, asnGuardMessage):
		return "asn_guard"
	case strings.HasPrefix(message, "title is required"):
		return "title_required"
	case errors.Is(err, fs.ErrNotExist):
		return "not_found"
	default:
		return "filesystem"
	}
}
