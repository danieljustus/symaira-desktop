package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	domain "github.com/danieljustus/symaira-desktop/internal/contacts/internal/domain/importer"
	"github.com/danieljustus/symaira-desktop/internal/contacts/internal/storage/sqlite"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

func TestPortContactsImportContract(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	seed := `INSERT INTO persons(id,display_name,given_name,family_name,notes,source) VALUES ('seed-ada','Ada','A','L','verified','manual'),('seed-bob','Bob','','','','manual');
 INSERT INTO contact_points(id,person_id,kind,raw_value,normalized_value) VALUES ('cp-a','seed-ada','email','ADA@example.com','ada@example.com'),('cp-b','seed-bob','phone','+49 (123)','+49123');
 INSERT INTO import_sources(id,source_kind,source_ref,person_id) VALUES ('src-a','csv','known','seed-ada');`
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	input := "name,email,phone,company,title,url\nAda,ADA@example.com,+49 123,Ignored,New,https://Example.com/\nNew,new@example.com;NEW@example.com,,Example,Engineer,https://site/\nNoName,,,,,\n"
	mapping := domain.DetectColumnMapping([]string{"name", "email", "phone", "company", "title", "url"})
	rows, issues, err := domain.ParseCSV(strings.NewReader(input), mapping)
	if err != nil {
		t.Fatal(err)
	}
	// Extra rows exercise exact source priority, unresolved duplicates, invalid
	// resolutions and merge-target validation independently of CSV syntax.
	rows = append(rows,
		domain.ImportRow{RowNumber: 4, DisplayName: "Renamed", SourceRef: "known", Emails: []string{"ada@example.com"}},
		domain.ImportRow{RowNumber: 5, DisplayName: "Bob", Phones: []string{"+49 (123)"}},
		domain.ImportRow{RowNumber: 6, DisplayName: "Ada", Emails: []string{"ada@example.com"}},
		domain.ImportRow{RowNumber: 7, DisplayName: "Bob"},
		domain.ImportRow{RowNumber: 8, DisplayName: "Ada"},
		domain.ImportRow{RowNumber: 9, DisplayName: "Ada"})
	resolutions := []domain.RowResolution{{RowNumber: 1, Resolution: domain.ResolutionMerge, MergePersonID: "seed-ada"}, {RowNumber: 3, Resolution: domain.ResolutionSkip}, {RowNumber: 4, Resolution: domain.ResolutionSkip}, {RowNumber: 6, Resolution: "unknown"}, {RowNumber: 7, Resolution: domain.ResolutionMerge}, {RowNumber: 8, Resolution: domain.ResolutionMerge, MergePersonID: "missing"}, {RowNumber: 9, Resolution: domain.ResolutionCreate}}
	svc := New(db, nil)
	plan, err := svc.Plan(ctx, domain.SourceCSV, rows, issues)
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Apply(ctx, plan, resolutions)
	if err != nil {
		t.Fatal(err)
	}
	// The only nondeterministic columns omitted from snapshots are UUID primary
	// keys and wall-clock timestamps. Foreign-key targets are represented by the
	// referenced person's/organization's name, retaining graph and raw data.
	queries := []string{
		`SELECT display_name,given_name,family_name,notes,source,source_ref FROM persons ORDER BY display_name,id`,
		`SELECT name,notes,source,source_ref FROM organizations ORDER BY name`,
		`SELECT p.display_name,cp.kind,cp.raw_value,cp.normalized_value,cp.label,cp.is_preferred,cp.is_verified FROM contact_points cp JOIN persons p ON p.id=cp.person_id ORDER BY p.display_name,cp.kind,cp.normalized_value`,
		`SELECT p.display_name,o.name,m.role,m.title,m.valid_from,m.valid_to FROM organization_memberships m JOIN persons p ON p.id=m.person_id JOIN organizations o ON o.id=m.organization_id ORDER BY p.display_name,o.name`,
		`SELECT s.source_kind,s.source_ref,p.display_name FROM import_sources s JOIN persons p ON p.id=s.person_id ORDER BY s.source_kind,s.source_ref`,
		`SELECT source_kind,created,merged,skipped,failed FROM import_runs ORDER BY rowid`,
	}
	snapshots := make([][][]any, 0, len(queries))
	for _, query := range queries {
		rs, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rs.Columns()
		if err != nil {
			t.Fatal(err)
		}
		snapshot := make([][]any, 0)
		for rs.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rs.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			snapshot = append(snapshot, values)
		}
		if err := rs.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rs.Close(); err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snapshot)
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../.."))
	hashes := map[string]string{}
	for _, rel := range []string{"internal/contacts/internal/service/importer/plan.go", "internal/contacts/internal/service/importer/apply.go", "internal/contacts/internal/service/importer/service.go", "internal/contacts/internal/domain/contact/normalize.go", "internal/contacts/internal/errs/errors.go"} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(data)
		hashes[rel] = hex.EncodeToString(h[:])
	}
	again, err := svc.Plan(ctx, domain.SourceCSV, rows, issues)
	if err != nil {
		t.Fatal(err)
	}
	reimport, err := svc.Apply(ctx, again, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture := struct {
		SourceHashes map[string]string      `json:"source_hashes"`
		Reimport     *domain.RunResult      `json:"reimport"`
		Oracle       any                    `json:"oracle"`
		Seed         string                 `json:"seed"`
		Plan         *domain.ImportPlan     `json:"plan"`
		Resolutions  []domain.RowResolution `json:"resolutions"`
		Result       *domain.RunResult      `json:"result"`
		Queries      []string               `json:"queries"`
		Snapshots    [][][]any              `json:"snapshots"`
	}{hashes, reimport, fixtureoracle.Current(), seed, plan, resolutions, result, queries, snapshots}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, "testdata/port/contacts/import.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	} else {
		expected, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(expected, data) {
			t.Fatal("contacts import fixture drift")
		}
	}
}
