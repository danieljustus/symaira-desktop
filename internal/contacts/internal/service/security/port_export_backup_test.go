package security

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/errs"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/storage/sqlite"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/google/uuid"
)

// CONTACT-001: data-minimized JSON/CSV export bytes, audit recording and the
// encrypted backup/restore format (AES-256-GCM, Argon2id) against the real Go
// service. The committed backup blob was written by Go; the Rust port must
// restore it, reject the damaged variants with Go's exact errors, and Go must
// restore a backup written by Rust (see TestPortContactsRustBackupRestores).
//
// Regenerate with:
// PORT_GENERATE=1 go test ./internal/contacts/internal/service/security -run '^TestPortContactsExportBackupContract$'

type portExportRand struct {
	n   byte
	ids []string
}

func (r *portExportRand) Read(p []byte) (int, error) {
	r.n++
	for i := range p {
		p[i] = 0
	}
	p[len(p)-1] = r.n
	u := append([]byte(nil), p...)
	u[6] = (u[6] & 15) | 64
	u[8] = (u[8] & 63) | 128
	id, err := uuid.FromBytes(u)
	if err != nil {
		return 0, err
	}
	r.ids = append(r.ids, id.String())
	return len(p), nil
}

const (
	portExportStamp      = "2026-01-02T03:04:05Z"
	portBackupPassphrase = "correct horse battery staple"
)

const portExportSeed = `
INSERT INTO persons(id,display_name,given_name,family_name,notes,created_at,updated_at) VALUES
 ('p1','Ada Lovelace','Ada','Lovelace','private note','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),
 ('p2','Bob, "The" Builder','','','','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),
 ('p3','line' || char(10) || 'break <&> name','L','B','','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),
 ('p4',' leading space','','','','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),
 ('p5','ärger Ünal','Ä','Ü','','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),
 ('p6','bob lower','','','','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),
 ('p7','Zed ; semi','','','','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),
 ('p9',char(12288) || 'ideographic space','','','','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),
 ('p10','Ada Lovelace','Other','Ada','','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),
 ('p11','json <b>&amp;</b> ' || char(8232) || char(9) || char(1) || char(127),'','','','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),
 ('p8','\.','','','','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z');
INSERT INTO contact_points(id,person_id,kind,raw_value,normalized_value,label,is_preferred,is_verified,created_at,updated_at) VALUES
 ('c1','p1','email','ada@example.com','ada@example.com','',0,0,'2026-01-02T03:04:01Z','2026-01-02T03:04:01Z'),
 ('c2','p1','email','ada.preferred@example.com','ada.preferred@example.com','',1,0,'2026-01-02T03:04:02Z','2026-01-02T03:04:02Z'),
 ('c3','p1','phone','+49 170 1234567','+491701234567','',0,0,'2026-01-02T03:04:03Z','2026-01-02T03:04:03Z'),
 ('c4','p1','address','1 Analytical Way','1 analytical way','',0,0,'2026-01-02T03:04:04Z','2026-01-02T03:04:04Z'),
 ('c5','p2','email','bob@example.com;evil','bob@example.com;evil','',0,0,'2026-01-02T03:04:01Z','2026-01-02T03:04:01Z'),
 ('c6','p3','phone','555 "0100"','5550100','',0,0,'2026-01-02T03:04:01Z','2026-01-02T03:04:01Z'),
 ('c7','p5','email','ärger@example.com','ärger@example.com','',0,0,'2026-01-02T03:04:01Z','2026-01-02T03:04:01Z');
INSERT INTO tags(id,name) VALUES ('t1','vip'),('t2','a,b'),('t3','Ünï');
INSERT INTO entity_tags(id,tag_id,person_id) VALUES ('e1','t1','p1'),('e2','t2','p1'),('e3','t3','p5'),('e4','t1','p2');
INSERT INTO organizations(id,name,created_at,updated_at) VALUES ('o1','Not Exported','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z');
`

type portBackupNegative struct {
	Name       string `json:"name"`
	Passphrase string `json:"passphrase"`
	BlobB64    string `json:"blob_b64"`
	Error      string `json:"error"`
	Kind       string `json:"kind"`
}

func portSnapshot(t *testing.T, db *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := [][]any{}
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = string(b)
			}
		}
		snapshot = append(snapshot, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

var portRestoreQueries = []string{
	"SELECT id, display_name, given_name, family_name, notes FROM persons ORDER BY id",
	"SELECT id, person_id, kind, raw_value, normalized_value, is_preferred FROM contact_points ORDER BY id",
	"SELECT t.name, et.person_id FROM tags t JOIN entity_tags et ON et.tag_id = t.id ORDER BY t.name, et.person_id",
	"SELECT id, name FROM organizations ORDER BY id",
	"SELECT operation, entity_type, entity_id, detail FROM audit_events ORDER BY id",
	"SELECT version, name FROM schema_migrations ORDER BY version",
}

func TestPortContactsExportBackupContract(t *testing.T) {
	ctx := context.Background()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../.."))
	path := filepath.Join(root, "testdata/port/contacts/export-backup.json")
	generate := os.Getenv("PORT_GENERATE") == "1"

	var stored struct {
		Backup struct {
			BlobB64 string `json:"blob_b64"`
		} `json:"backup"`
	}
	if !generate {
		raw, err := os.ReadFile(path) //nolint:gosec // fixed fixture path
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatal(err)
		}
	}

	entropy := &portExportRand{}
	uuid.SetRand(entropy)
	t.Cleanup(func() { uuid.SetRand(nil) })

	db, err := sqlite.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := New(db)

	// Empty database first: nil slice encodes as JSON null, CSV is the header.
	var emptyJSON, emptyCSV bytes.Buffer
	if err := svc.ExportJSON(ctx, &emptyJSON); err != nil {
		t.Fatal(err)
	}
	if err := svc.ExportCSV(ctx, &emptyCSV); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(portExportSeed); err != nil {
		t.Fatal(err)
	}
	var fullJSON, fullCSV bytes.Buffer
	if err := svc.ExportJSON(ctx, &fullJSON); err != nil {
		t.Fatal(err)
	}
	if err := svc.ExportCSV(ctx, &fullCSV); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE audit_events SET occurred_at = ?, actor = 'actor'", portExportStamp); err != nil {
		t.Fatal(err)
	}
	auditAfterExports := portSnapshot(t, db, "SELECT id, occurred_at, operation, actor, entity_type, entity_id, detail FROM audit_events ORDER BY id")

	// Backup: a Go-written blob is committed; check mode restores that blob.
	var blob []byte
	if generate {
		var buf bytes.Buffer
		if err := svc.Backup(ctx, []byte(portBackupPassphrase), &buf); err != nil {
			t.Fatal(err)
		}
		blob = buf.Bytes()
	} else {
		blob, err = base64.StdEncoding.DecodeString(stored.Backup.BlobB64)
		if err != nil {
			t.Fatal(err)
		}
		// The live oracle must still produce a restorable backup.
		var live bytes.Buffer
		if err := svc.Backup(ctx, []byte(portBackupPassphrase), &live); err != nil {
			t.Fatal(err)
		}
		if live.Len() == 0 || !bytes.HasPrefix(live.Bytes(), backupMagic[:]) {
			t.Fatal("live backup lacks the format magic")
		}
	}
	backupAudit := portSnapshot(t, db, "SELECT operation, entity_type, entity_id, detail FROM audit_events WHERE operation = 'backup_created' ORDER BY id")

	restore := func(passphrase string, data []byte, target string) error {
		return Restore(ctx, []byte(passphrase), bytes.NewReader(data), target)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "restored.db")
	if err := restore(portBackupPassphrase, blob, target); err != nil {
		t.Fatalf("restore of committed blob: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := sqlite.Open(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	snapshots := [][][]any{}
	for _, q := range portRestoreQueries {
		snapshots = append(snapshots, portSnapshot(t, restored, q))
	}
	_ = restored.Close()
	if _, err := os.Stat(target + ".restoring"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restoring temp file left behind: %v", err)
	}

	flip := func(b []byte, i int) []byte {
		c := append([]byte(nil), b...)
		c[i] ^= 0x01
		return c
	}
	negatives := []portBackupNegative{}
	addNegative := func(name, passphrase string, data []byte) {
		neg := filepath.Join(dir, fmt.Sprintf("neg-%d.db", len(negatives)))
		err := restore(passphrase, data, neg)
		if err == nil {
			t.Fatalf("negative case %s unexpectedly restored", name)
		}
		if _, statErr := os.Stat(neg); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("negative case %s touched the target: %v", name, statErr)
		}
		negatives = append(negatives, portBackupNegative{name, passphrase, base64.StdEncoding.EncodeToString(data), err.Error(), string(errs.KindOf(err))})
	}
	addNegative("empty-passphrase", "", blob)
	addNegative("wrong-passphrase", "wrong", blob)
	addNegative("empty-input", portBackupPassphrase, nil)
	addNegative("truncated-header", portBackupPassphrase, blob[:34])
	addNegative("header-only", portBackupPassphrase, blob[:35])
	addNegative("bad-magic", portBackupPassphrase, flip(blob, 0))
	addNegative("bad-version-byte", portBackupPassphrase, flip(blob, 5))
	addNegative("tampered-salt", portBackupPassphrase, flip(blob, 8))
	addNegative("tampered-nonce", portBackupPassphrase, flip(blob, 25))
	addNegative("tampered-ciphertext", portBackupPassphrase, flip(blob, 40))
	addNegative("tampered-tag", portBackupPassphrase, flip(blob, len(blob)-1))
	addNegative("truncated-ciphertext", portBackupPassphrase, blob[:len(blob)-1])

	// A Backup with no passphrase is rejected before anything is written.
	var rejected bytes.Buffer
	backupErr := svc.Backup(ctx, nil, &rejected)
	if backupErr == nil || rejected.Len() != 0 {
		t.Fatalf("empty-passphrase backup: %v (%d bytes)", backupErr, rejected.Len())
	}

	// Go cannot scan a NULL name column; the export fails closed and records nothing.
	nullDB, err := sqlite.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = nullDB.Close() }()
	if _, err := nullDB.Exec("INSERT INTO persons(id,display_name,given_name,created_at,updated_at) VALUES ('n1','Null Given',NULL,'2026-01-02T03:04:05Z','2026-01-02T03:04:05Z')"); err != nil {
		t.Fatal(err)
	}
	var nullOut bytes.Buffer
	nullJSONErr := New(nullDB).ExportJSON(ctx, &nullOut)
	nullCSVErr := New(nullDB).ExportCSV(ctx, &nullOut)
	if nullJSONErr == nil || nullCSVErr == nil || nullOut.Len() != 0 {
		t.Fatalf("NULL name export: %v / %v (%d bytes)", nullJSONErr, nullCSVErr, nullOut.Len())
	}
	if n := len(portSnapshot(t, nullDB, "SELECT id FROM audit_events")); n != 0 {
		t.Fatalf("failed export recorded %d audit events", n)
	}

	sum := sha256.Sum256(blob)
	hashes := map[string]string{}
	for _, rel := range []string{"internal/contacts/internal/service/security/backup.go", "internal/contacts/internal/service/security/export.go", "internal/contacts/internal/service/security/audit.go"} {
		b, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // fixed repository paths
		if err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256(b)
		hashes[rel] = fmt.Sprintf("%x", s)
	}
	fixture := map[string]any{
		"oracle": fixtureoracle.Current(), "source_hashes": hashes, "timestamp": portExportStamp, "ids": entropy.ids[:len(entropy.ids)],
		"seed": portExportSeed,
		"export": map[string]any{
			"empty_json": emptyJSON.String(), "empty_csv": emptyCSV.String(),
			"json": fullJSON.String(), "csv": fullCSV.String(),
			"audit_after_exports":  auditAfterExports,
			"null_name_json_error": nullJSONErr.Error(), "null_name_csv_error": nullCSVErr.Error(), "null_name_kind": string(errs.KindOf(nullJSONErr)),
		},
		"backup": map[string]any{
			"passphrase": portBackupPassphrase, "blob_b64": base64.StdEncoding.EncodeToString(blob), "blob_sha256": fmt.Sprintf("%x", sum),
			"blob_len": len(blob), "plaintext_len": len(blob) - len(backupMagic) - saltSize - 12 - 16,
			"backup_audit_shape": backupAudit, "restored_mode": fmt.Sprintf("%o", info.Mode().Perm()),
			"restore_queries": portRestoreQueries, "restore_snapshots": snapshots, "negatives": negatives,
			"backup_error": backupErr.Error(), "backup_error_kind": string(errs.KindOf(backupErr)),
		},
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if generate {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		expected, err := os.ReadFile(path) //nolint:gosec // fixed fixture path
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(expected, data) {
			t.Fatal("contacts export/backup fixture drift")
		}
	}
	fmt.Printf("contacts export/backup: %d negatives, %d restore snapshots\n", len(negatives), len(snapshots))
}

// TestPortContactsRustBackupRestores proves the reverse direction: a backup
// written by the Rust port (path in PORT_RUST_BACKUP) restores in Go with the
// shared passphrase. It is exercised by `make contacts-security-differential`,
// which sets the variable after the Rust test has written the file.
func TestPortContactsRustBackupRestores(t *testing.T) {
	path := os.Getenv("PORT_RUST_BACKUP")
	if path == "" {
		t.Skip("PORT_RUST_BACKUP not set; exercised by make contacts-security-differential")
	}
	ctx := context.Background()
	data, err := os.ReadFile(path) //nolint:gosec // test-owned exchange file
	if err != nil {
		t.Fatalf("Rust did not write the exchange backup: %v", err)
	}
	if !bytes.HasPrefix(data, backupMagic[:]) {
		t.Fatal("Rust backup lacks the format magic")
	}
	target := filepath.Join(t.TempDir(), "from-rust.db")
	if err := Restore(ctx, []byte(portBackupPassphrase), bytes.NewReader(data), target); err != nil {
		t.Fatalf("Go cannot restore the Rust backup: %v", err)
	}
	restored, err := sqlite.Open(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	got := portSnapshot(t, restored, "SELECT id, display_name FROM persons ORDER BY id")
	want := [][]any{{"p1", "Ada Lovelace"}, {"p2", `Bob, "The" Builder`}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("restored persons = %v, want %v", got, want)
	}
	audit := portSnapshot(t, restored, "SELECT operation FROM audit_events ORDER BY id")
	if len(audit) < 2 || audit[len(audit)-1][0] != "backup_restored" {
		t.Fatalf("audit trail = %v, want it to end with backup_restored", audit)
	}
}
