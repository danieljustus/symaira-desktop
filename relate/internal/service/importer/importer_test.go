package importer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contactdomain "github.com/danieljustus/symaira-relate/internal/domain/contact"
	importerdomain "github.com/danieljustus/symaira-relate/internal/domain/importer"
	contactsvc "github.com/danieljustus/symaira-relate/internal/service/contact"
	"github.com/danieljustus/symaira-relate/internal/storage/sqlite"
)

func contactPersonInput(displayName string) contactdomain.PersonInput {
	return contactdomain.PersonInput{DisplayName: displayName}
}

func contactPointInput(email string) contactdomain.ContactPointInput {
	return contactdomain.ContactPointInput{Kind: contactdomain.ContactPointEmail, RawValue: email}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	// internal/service/importer -> repo root
	return filepath.Join(wd, "..", "..", "..")
}

func newFixture(t *testing.T) *Service {
	t.Helper()
	db, err := sqlite.OpenMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenMemory() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db, contactsvc.New(db))
}

func TestPlan_DryRunNeverWrites(t *testing.T) {
	ctx := context.Background()
	s := newFixture(t)

	f, err := os.Open(filepath.Join(repoRoot(t), "testdata", "import", "sample.vcf"))
	if err != nil {
		t.Fatalf("open fixture error = %v", err)
	}
	defer f.Close()
	rows, issues, err := importerdomain.ParseVCard(f)
	if err != nil {
		t.Fatalf("ParseVCard() error = %v", err)
	}

	plan, err := s.Plan(ctx, importerdomain.SourceVCard, rows, issues)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.Rows) != 3 {
		t.Fatalf("len(plan.Rows) = %d, want 3", len(plan.Rows))
	}

	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM persons").Scan(&count); err != nil {
		t.Fatalf("query persons error = %v", err)
	}
	if count != 0 {
		t.Errorf("persons count after Plan() = %d, want 0 (dry run must not write)", count)
	}
}

func TestApply_ImportsVCardFixturePredictably(t *testing.T) {
	ctx := context.Background()
	s := newFixture(t)

	f, err := os.Open(filepath.Join(repoRoot(t), "testdata", "import", "sample.vcf"))
	if err != nil {
		t.Fatalf("open fixture error = %v", err)
	}
	defer f.Close()
	rows, issues, err := importerdomain.ParseVCard(f)
	if err != nil {
		t.Fatalf("ParseVCard() error = %v", err)
	}
	plan, err := s.Plan(ctx, importerdomain.SourceVCard, rows, issues)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.Duplicates) != 0 {
		t.Fatalf("plan.Duplicates = %+v, want none on an empty database", plan.Duplicates)
	}

	result, err := s.Apply(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Created != 3 || result.Failed != 0 {
		t.Fatalf("result = %+v, want 3 created, 0 failed", result)
	}

	var personCount, orgCount, cpCount int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM persons").Scan(&personCount)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM organizations").Scan(&orgCount)
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM contact_points").Scan(&cpCount)
	if personCount != 3 {
		t.Errorf("persons = %d, want 3", personCount)
	}
	if orgCount != 1 {
		t.Errorf("organizations = %d, want 1 (two cards share \"Example Co\")", orgCount)
	}
	if cpCount == 0 {
		t.Error("contact_points = 0, want > 0")
	}

	var membershipCount int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM organization_memberships").Scan(&membershipCount)
	if membershipCount != 2 {
		t.Errorf("memberships = %d, want 2 (Jordan and Morgan both belong to Example Co)", membershipCount)
	}
}

func TestApply_InvalidRowsNeverReachTheDatabase(t *testing.T) {
	ctx := context.Background()
	s := newFixture(t)

	f, err := os.Open(filepath.Join(repoRoot(t), "testdata", "import", "sample.csv"))
	if err != nil {
		t.Fatalf("open fixture error = %v", err)
	}
	defer f.Close()
	header := importerdomain.DetectColumnMapping([]string{"Full Name", "Email", "Phone", "Company"})
	rows, issues, err := importerdomain.ParseCSV(f, header)
	if err != nil {
		t.Fatalf("ParseCSV() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %+v, want exactly 1 (the nameless row)", issues)
	}
	if issues[0].RowNumber != 3 {
		t.Errorf("issue row = %d, want 3", issues[0].RowNumber)
	}

	plan, err := s.Plan(ctx, importerdomain.SourceCSV, rows, issues)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	result, err := s.Apply(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Created != 2 {
		t.Fatalf("result.Created = %d, want 2 (only the 2 valid rows)", result.Created)
	}

	var count int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM persons WHERE display_name = ''").Scan(&count)
	if count != 0 {
		t.Errorf("found %d persons created from the invalid row, want 0", count)
	}
}

func TestApply_ReImportSameVCardIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newFixture(t)

	parseAndApply := func() *importerdomain.RunResult {
		f, err := os.Open(filepath.Join(repoRoot(t), "testdata", "import", "sample.vcf"))
		if err != nil {
			t.Fatalf("open fixture error = %v", err)
		}
		defer f.Close()
		rows, issues, err := importerdomain.ParseVCard(f)
		if err != nil {
			t.Fatalf("ParseVCard() error = %v", err)
		}
		plan, err := s.Plan(ctx, importerdomain.SourceVCard, rows, issues)
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		result, err := s.Apply(ctx, plan, nil)
		if err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
		return result
	}

	first := parseAndApply()
	if first.Created != 3 {
		t.Fatalf("first import: Created = %d, want 3", first.Created)
	}

	// Second import of the identical source: every row now matches its
	// own prior import via import_sources, so re-running without any
	// resolution must skip everything, not duplicate it.
	f, err := os.Open(filepath.Join(repoRoot(t), "testdata", "import", "sample.vcf"))
	if err != nil {
		t.Fatalf("open fixture error = %v", err)
	}
	defer f.Close()
	rows, issues, err := importerdomain.ParseVCard(f)
	if err != nil {
		t.Fatalf("ParseVCard() error = %v", err)
	}
	plan, err := s.Plan(ctx, importerdomain.SourceVCard, rows, issues)
	if err != nil {
		t.Fatalf("second Plan() error = %v", err)
	}
	if len(plan.Duplicates) == 0 {
		t.Fatal("second Plan() found no duplicates, want an exact-source-rematch candidate per row")
	}
	for _, d := range plan.Duplicates {
		if !d.ExactSourceRematch {
			t.Errorf("duplicate %+v is not an exact source rematch on a byte-identical re-import", d)
		}
	}

	second, err := s.Apply(ctx, plan, nil)
	if err != nil {
		t.Fatalf("second Apply() error = %v", err)
	}
	if second.Created != 0 || second.Skipped != 3 {
		t.Fatalf("second import = %+v, want 0 created, 3 skipped (idempotent)", second)
	}

	var personCount int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM persons").Scan(&personCount)
	if personCount != 3 {
		t.Errorf("persons after re-import = %d, want still 3", personCount)
	}
}

func TestApply_DuplicateCandidateRequiresExplicitResolution(t *testing.T) {
	ctx := context.Background()
	s := newFixture(t)

	existing, err := s.contacts.CreatePerson(ctx, contactPersonInput("Jordan Example"))
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}
	if _, err := s.contacts.AddPersonContactPoint(ctx, existing.ID, contactPointInput("jordan@example.com")); err != nil {
		t.Fatalf("AddPersonContactPoint() error = %v", err)
	}

	row := importerdomain.ImportRow{RowNumber: 1, DisplayName: "Jordan Example", Emails: []string{"jordan@example.com"}, SourceRef: "csv:test-row"}
	plan, err := s.Plan(ctx, importerdomain.SourceCSV, []importerdomain.ImportRow{row}, nil)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.Duplicates) == 0 {
		t.Fatal("Plan() found no duplicate for an exact email match")
	}

	// No resolution given: must default to skip, never silently create or
	// merge.
	result, err := s.Apply(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Apply() with no resolution error = %v", err)
	}
	if result.Created != 0 || result.Skipped != 1 {
		t.Fatalf("result = %+v, want 0 created, 1 skipped", result)
	}

	// Explicit merge resolution.
	result, err = s.Apply(ctx, plan, []importerdomain.RowResolution{
		{RowNumber: 1, Resolution: importerdomain.ResolutionMerge, MergePersonID: existing.ID},
	})
	if err != nil {
		t.Fatalf("Apply() with merge resolution error = %v", err)
	}
	if result.Merged != 1 {
		t.Fatalf("result = %+v, want 1 merged", result)
	}

	var personCount int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM persons").Scan(&personCount)
	if personCount != 1 {
		t.Errorf("persons after merge = %d, want still 1 (merge must not create a new person)", personCount)
	}
}

func TestApply_ExplicitCreateOverridesDuplicateWarning(t *testing.T) {
	ctx := context.Background()
	s := newFixture(t)

	existing, err := s.contacts.CreatePerson(ctx, contactPersonInput("Jordan Example"))
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}
	if _, err := s.contacts.AddPersonContactPoint(ctx, existing.ID, contactPointInput("jordan@example.com")); err != nil {
		t.Fatalf("AddPersonContactPoint() error = %v", err)
	}

	row := importerdomain.ImportRow{RowNumber: 1, DisplayName: "Jordan Example", Emails: []string{"jordan@example.com"}, SourceRef: "csv:another-row"}
	plan, err := s.Plan(ctx, importerdomain.SourceCSV, []importerdomain.ImportRow{row}, nil)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	result, err := s.Apply(ctx, plan, []importerdomain.RowResolution{{RowNumber: 1, Resolution: importerdomain.ResolutionCreate}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Created != 1 {
		t.Fatalf("result = %+v, want 1 created", result)
	}

	var personCount int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM persons").Scan(&personCount)
	if personCount != 2 {
		t.Errorf("persons = %d, want 2 (explicit create must add a second person)", personCount)
	}
}

func TestListRuns_RecordsApplyOutcome(t *testing.T) {
	ctx := context.Background()
	s := newFixture(t)

	f, err := os.Open(filepath.Join(repoRoot(t), "testdata", "import", "sample.vcf"))
	if err != nil {
		t.Fatalf("open fixture error = %v", err)
	}
	defer f.Close()
	rows, issues, err := importerdomain.ParseVCard(f)
	if err != nil {
		t.Fatalf("ParseVCard() error = %v", err)
	}
	plan, err := s.Plan(ctx, importerdomain.SourceVCard, rows, issues)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if _, err := s.Apply(ctx, plan, nil); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	runs, err := s.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 || runs[0].Created != 3 {
		t.Fatalf("runs = %+v, want 1 run with Created=3", runs)
	}
}

// TestApply_ImportedPersonsAreReadableThroughContactService guards against
// a raw INSERT in apply.go omitting a nullable column (label, notes,
// role, ...): the contact service scans those columns into plain Go
// strings, which panics with "converting NULL to string is unsupported"
// if a writer leaves them NULL instead of empty string. Every write path
// must produce rows the ordinary contact/org/membership read paths can
// load.
func TestApply_ImportedPersonsAreReadableThroughContactService(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.OpenMemory(ctx)
	if err != nil {
		t.Fatalf("OpenMemory() error = %v", err)
	}
	defer db.Close()
	contacts := contactsvc.New(db)
	s := New(db, contacts)

	f, err := os.Open(filepath.Join(repoRoot(t), "testdata", "import", "sample.vcf"))
	if err != nil {
		t.Fatalf("open fixture error = %v", err)
	}
	defer f.Close()
	rows, issues, err := importerdomain.ParseVCard(f)
	if err != nil {
		t.Fatalf("ParseVCard() error = %v", err)
	}
	plan, err := s.Plan(ctx, importerdomain.SourceVCard, rows, issues)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if _, err := s.Apply(ctx, plan, nil); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	list, err := contacts.ListPersons(ctx, contactsvc.ListPersonsOptions{})
	if err != nil {
		t.Fatalf("ListPersons() error = %v", err)
	}
	if len(list.Items) != 3 {
		t.Fatalf("len(list.Items) = %d, want 3", len(list.Items))
	}
	for _, p := range list.Items {
		full, err := contacts.GetPerson(ctx, p.ID)
		if err != nil {
			t.Fatalf("GetPerson(%s) error = %v", p.ID, err)
		}
		if _, err := contacts.ListMembershipsByPerson(ctx, full.ID); err != nil {
			t.Fatalf("ListMembershipsByPerson(%s) error = %v", full.ID, err)
		}
	}
}

// TestApplyCreate_CreateFromRowFailureIsRecordedPerRow covers applyCreate's
// createFromRow error branch (apply.go:76-79): the failed row must be counted
// in RunResult.Failed and surfaced as a per-row ValidationIssue carrying the
// row number and the underlying error, while Created stays untouched.
//
// A brand-new person row cannot violate any reachable constraint (persons has
// no CHECK/UNIQUE constraints and every column is a plain string), so the
// failure is forced through a committed transaction — the same white-box
// helper approach quickadd_test.go documents for branches the public API
// cannot reach.
func TestApplyCreate_CreateFromRowFailureIsRecordedPerRow(t *testing.T) {
	ctx := context.Background()
	s := newFixture(t)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	row := importerdomain.ImportRow{RowNumber: 4, DisplayName: "Failing Row"}
	result := &importerdomain.RunResult{Source: importerdomain.SourceCSV}
	s.applyCreate(ctx, tx, importerdomain.SourceCSV, row, result)

	if result.Failed != 1 {
		t.Errorf("result.Failed = %d, want 1", result.Failed)
	}
	if result.Created != 0 {
		t.Errorf("result.Created = %d, want 0 (nothing may be created)", result.Created)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("len(result.Errors) = %d, want 1: %+v", len(result.Errors), result.Errors)
	}
	if got := result.Errors[0]; got.RowNumber != row.RowNumber || got.Field != "" || got.Message == "" {
		t.Errorf("ValidationIssue = %+v, want RowNumber=%d with a non-empty message", got, row.RowNumber)
	}
}

// TestApplyCreate_LinkSourceFailureIsRecordedPerRowAndKeepsPerson covers
// applyCreate's linkSource error branch (apply.go:81-85): createFromRow has
// already committed its person INSERT inside the transaction, and when the
// source link then fails the row is counted as failed without rolling back
// the already-succeeded creation and without aborting the run.
//
// A bogus SourceKind trips the import_sources.source_kind CHECK constraint;
// this is the only deterministic way to make linkSource fail, and it exercises
// the exact data-mutating path a real importer bug would take.
func TestApplyCreate_LinkSourceFailureIsRecordedPerRowAndKeepsPerson(t *testing.T) {
	ctx := context.Background()
	s := newFixture(t)

	row := importerdomain.ImportRow{RowNumber: 1, DisplayName: "Link Fail", SourceRef: "csv:link-fail"}
	result := &importerdomain.RunResult{Source: importerdomain.SourceCSV}
	err := sqlite.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		s.applyCreate(ctx, tx, importerdomain.SourceKind("bogus"), row, result)
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx() error = %v", err)
	}

	if result.Failed != 1 {
		t.Errorf("result.Failed = %d, want 1", result.Failed)
	}
	if result.Created != 0 {
		t.Errorf("result.Created = %d, want 0 (link failure means no successful create)", result.Created)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("len(result.Errors) = %d, want 1: %+v", len(result.Errors), result.Errors)
	}
	if got := result.Errors[0]; got.RowNumber != row.RowNumber || got.Field != "" || !strings.Contains(got.Message, "CHECK constraint") {
		t.Errorf("ValidationIssue = %+v, want RowNumber=%d with the constraint error", got, row.RowNumber)
	}

	// The person INSERT from createFromRow must survive: a per-row failure
	// only skips that row, it never aborts the run.
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM persons WHERE display_name = ?", row.DisplayName).Scan(&count); err != nil {
		t.Fatalf("query persons error = %v", err)
	}
	if count != 1 {
		t.Errorf("persons with display_name %q = %d, want 1 (create must not be rolled back)", row.DisplayName, count)
	}
}

// TestApply_MergeResolutionWithEmptyTargetIsRecorded covers applyMerge's
// empty-target guard (apply.go:90-93): a merge resolution without a target
// person id is a per-row failure with a Field-specific ValidationIssue, not a
// silent no-op.
func TestApply_MergeResolutionWithEmptyTargetIsRecorded(t *testing.T) {
	ctx := context.Background()
	s := newFixture(t)

	existing, err := s.contacts.CreatePerson(ctx, contactPersonInput("Jordan Example"))
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}
	if _, err := s.contacts.AddPersonContactPoint(ctx, existing.ID, contactPointInput("jordan@example.com")); err != nil {
		t.Fatalf("AddPersonContactPoint() error = %v", err)
	}

	row := importerdomain.ImportRow{RowNumber: 1, DisplayName: "Jordan Example", Emails: []string{"jordan@example.com"}, SourceRef: "csv:empty-target"}
	plan, err := s.Plan(ctx, importerdomain.SourceCSV, []importerdomain.ImportRow{row}, nil)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.Duplicates) == 0 {
		t.Fatal("Plan() found no duplicate for an exact email match")
	}

	result, err := s.Apply(ctx, plan, []importerdomain.RowResolution{
		{RowNumber: 1, Resolution: importerdomain.ResolutionMerge, MergePersonID: ""},
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Failed != 1 || result.Merged != 0 {
		t.Fatalf("result = %+v, want 1 failed, 0 merged", result)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("len(result.Errors) = %d, want 1: %+v", len(result.Errors), result.Errors)
	}
	want := importerdomain.ValidationIssue{
		RowNumber: 1,
		Field:     "merge_person_id",
		Message:   "merge resolution requires a target person id",
	}
	if got := result.Errors[0]; got != want {
		t.Errorf("ValidationIssue = %+v, want %+v", got, want)
	}

	var personCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM persons").Scan(&personCount); err != nil {
		t.Fatalf("query persons error = %v", err)
	}
	if personCount != 1 {
		t.Errorf("persons after failed merge = %d, want still 1", personCount)
	}
}

// TestApply_MergeResolutionWithMissingTargetIsRecorded covers applyMerge's
// merge-with-missing-target branch (apply.go:95-99): merging into a person id
// that does not exist is a per-row failure recorded in RunResult, and the row
// must not create a new person.
func TestApply_MergeResolutionWithMissingTargetIsRecorded(t *testing.T) {
	ctx := context.Background()
	s := newFixture(t)

	existing, err := s.contacts.CreatePerson(ctx, contactPersonInput("Jordan Example"))
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}
	if _, err := s.contacts.AddPersonContactPoint(ctx, existing.ID, contactPointInput("jordan@example.com")); err != nil {
		t.Fatalf("AddPersonContactPoint() error = %v", err)
	}

	row := importerdomain.ImportRow{RowNumber: 1, DisplayName: "Jordan Example", Emails: []string{"jordan@example.com"}, SourceRef: "csv:missing-target"}
	plan, err := s.Plan(ctx, importerdomain.SourceCSV, []importerdomain.ImportRow{row}, nil)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.Duplicates) == 0 {
		t.Fatal("Plan() found no duplicate for an exact email match")
	}

	result, err := s.Apply(ctx, plan, []importerdomain.RowResolution{
		{RowNumber: 1, Resolution: importerdomain.ResolutionMerge, MergePersonID: "no-such-person"},
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Failed != 1 || result.Merged != 0 {
		t.Fatalf("result = %+v, want 1 failed, 0 merged", result)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("len(result.Errors) = %d, want 1: %+v", len(result.Errors), result.Errors)
	}
	if got := result.Errors[0]; got.RowNumber != 1 || !strings.Contains(got.Message, "merge target person does not exist") {
		t.Errorf("ValidationIssue = %+v, want RowNumber=1 with missing-target message", got)
	}

	var personCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM persons").Scan(&personCount); err != nil {
		t.Fatalf("query persons error = %v", err)
	}
	if personCount != 1 {
		t.Errorf("persons after failed merge = %d, want still 1", personCount)
	}
}

// TestApplyMerge_LinkSourceFailureIsRecordedPerRow covers applyMerge's
// linkSource error branch (apply.go:100-104): when the merge itself succeeds
// but the source link then fails, the row is counted as failed with the
// constraint error, and the merged contact points survive.
func TestApplyMerge_LinkSourceFailureIsRecordedPerRow(t *testing.T) {
	ctx := context.Background()
	s := newFixture(t)

	existing, err := s.contacts.CreatePerson(ctx, contactPersonInput("Jordan Example"))
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}
	if _, err := s.contacts.AddPersonContactPoint(ctx, existing.ID, contactPointInput("jordan@example.com")); err != nil {
		t.Fatalf("AddPersonContactPoint() error = %v", err)
	}

	row := importerdomain.ImportRow{RowNumber: 1, DisplayName: "Jordan Example", Emails: []string{"jordan@example.com"}, Phones: []string{"+49 30 123456"}, SourceRef: "csv:merge-link-fail"}
	result := &importerdomain.RunResult{Source: importerdomain.SourceCSV}
	err = sqlite.WithTx(ctx, s.db, func(tx *sql.Tx) error {
		s.applyMerge(ctx, tx, importerdomain.SourceKind("bogus"), row, existing.ID, result)
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx() error = %v", err)
	}

	if result.Failed != 1 || result.Merged != 0 {
		t.Fatalf("result = %+v, want 1 failed, 0 merged", result)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("len(result.Errors) = %d, want 1: %+v", len(result.Errors), result.Errors)
	}
	if got := result.Errors[0]; got.RowNumber != row.RowNumber || got.Field != "" || !strings.Contains(got.Message, "CHECK constraint") {
		t.Errorf("ValidationIssue = %+v, want RowNumber=%d with the constraint error", got, row.RowNumber)
	}

	// The mergeIntoRow contact-point INSERT must survive the link failure.
	var phoneCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM contact_points WHERE person_id = ? AND kind = ?", existing.ID, contactdomain.ContactPointPhone).Scan(&phoneCount); err != nil {
		t.Fatalf("query contact points error = %v", err)
	}
	if phoneCount != 1 {
		t.Errorf("phones on target = %d, want 1 (merge must not be rolled back)", phoneCount)
	}
}

// TestApply_MergeSuccessfullyMergesContactPointsAndLinksSource covers
// applyMerge's happy path (apply.go:100-105): the merge adds the row's contact
// points to the target person, records Merged++, and links the import source
// to the target person.
func TestApply_MergeSuccessfullyMergesContactPointsAndLinksSource(t *testing.T) {
	ctx := context.Background()
	s := newFixture(t)

	existing, err := s.contacts.CreatePerson(ctx, contactPersonInput("Jordan Example"))
	if err != nil {
		t.Fatalf("CreatePerson() error = %v", err)
	}
	if _, err := s.contacts.AddPersonContactPoint(ctx, existing.ID, contactPointInput("jordan@example.com")); err != nil {
		t.Fatalf("AddPersonContactPoint() error = %v", err)
	}

	row := importerdomain.ImportRow{
		RowNumber: 1, DisplayName: "Jordan Example",
		Emails: []string{"jordan@example.com"}, Phones: []string{"+49 30 123456"},
		SourceRef: "csv:merge-ok",
	}
	plan, err := s.Plan(ctx, importerdomain.SourceCSV, []importerdomain.ImportRow{row}, nil)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.Duplicates) == 0 {
		t.Fatal("Plan() found no duplicate for an exact email match")
	}

	result, err := s.Apply(ctx, plan, []importerdomain.RowResolution{
		{RowNumber: 1, Resolution: importerdomain.ResolutionMerge, MergePersonID: existing.ID},
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Merged != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want 1 merged, 0 failed", result)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("result.Errors = %+v, want none", result.Errors)
	}

	// Merge must not create a new person.
	var personCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM persons").Scan(&personCount); err != nil {
		t.Fatalf("query persons error = %v", err)
	}
	if personCount != 1 {
		t.Errorf("persons after merge = %d, want still 1", personCount)
	}

	// The row's phone must now live on the target person (the email already
	// existed and is preserved).
	var phoneCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM contact_points WHERE person_id = ? AND kind = ?", existing.ID, contactdomain.ContactPointPhone).Scan(&phoneCount); err != nil {
		t.Fatalf("query contact points error = %v", err)
	}
	if phoneCount != 1 {
		t.Errorf("phones on target = %d, want 1 (merged)", phoneCount)
	}

	// The import source must be linked to the target person for idempotency.
	var linkedPersonID string
	if err := s.db.QueryRowContext(ctx, "SELECT person_id FROM import_sources WHERE source_kind = ? AND source_ref = ?", importerdomain.SourceCSV, row.SourceRef).Scan(&linkedPersonID); err != nil {
		t.Fatalf("query import_sources error = %v", err)
	}
	if linkedPersonID != existing.ID {
		t.Errorf("import_sources person_id = %q, want %q", linkedPersonID, existing.ID)
	}
}
