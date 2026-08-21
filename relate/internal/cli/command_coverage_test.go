package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/domain/page"
	"github.com/danieljustus/symaira-relate/internal/domain/relationship"
)

// -- import --------------------------------------------------------------

func writeImportFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write import file: %v", err)
	}
	return path
}

const importVCF = "BEGIN:VCARD\r\n" +
	"VERSION:3.0\r\n" +
	"FN:Jordan Example\r\n" +
	"N:Example;Jordan;;;\r\n" +
	"EMAIL:jordan@example.com\r\n" +
	"UID:cli-import-vcard-001\r\n" +
	"END:VCARD\r\n"

func TestImportVCard_DryRunAndApply(t *testing.T) {
	setTestProfileDirs(t)
	vcf := writeImportFile(t, "c.vcf", importVCF)

	// Dry run: plan printed, nothing written.
	out, stderr, code := runCLI(t, "import", "vcard", "--dry-run", vcf)
	if code != 0 {
		t.Fatalf("import vcard --dry-run: code=%d stderr=%s", code, stderr)
	}
	var plan map[string]any
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("dry-run output not JSON: %v (%s)", err, out)
	}
	rows, _ := plan["Rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("dry-run plan rows = %v, want 1", plan["Rows"])
	}

	// Real import: contact created.
	out, stderr, code = runCLI(t, "import", "vcard", vcf)
	if code != 0 {
		t.Fatalf("import vcard: code=%d stderr=%s", code, stderr)
	}
	// The contact is now visible.
	out, stderr, code = runCLI(t, "contact", "list", "--query", "Jordan")
	if code != 0 || !strings.Contains(out, "Jordan Example") {
		t.Fatalf("contact list after import: code=%d out=%s stderr=%s", code, out, stderr)
	}

	// Re-import is idempotent and reports a duplicate candidate.
	out, stderr, code = runCLI(t, "import", "vcard", vcf)
	if code != 0 {
		t.Fatalf("re-import: code=%d stderr=%s", code, stderr)
	}
	var applied struct {
		Plan struct {
			Duplicates []any `json:"Duplicates"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(out), &applied); err != nil {
		t.Fatalf("apply output not JSON: %v (%s)", err, out)
	}
	if len(applied.Plan.Duplicates) == 0 {
		t.Errorf("re-import plan has no duplicate candidates: %s", out)
	}

	// Import runs lists the runs.
	out, stderr, code = runCLI(t, "import", "runs")
	if code != 0 {
		t.Fatalf("import runs: code=%d stderr=%s", code, stderr)
	}
	var runs []any
	if err := json.Unmarshal([]byte(out), &runs); err != nil || len(runs) == 0 {
		t.Fatalf("import runs output = %q, want >= 1 run", out)
	}
}

func TestImportCSV_AutoDetectedAndExplicitMapping(t *testing.T) {
	setTestProfileDirs(t)

	// Auto-detected mapping from the header.
	csvPath := writeImportFile(t, "c.csv", "name,email\nJane Doe,jane@example.com\n")
	out, stderr, code := runCLI(t, "import", "csv", "--dry-run", csvPath)
	if code != 0 {
		t.Fatalf("import csv auto: code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "column mapping:") {
		t.Errorf("csv import stderr missing mapping line: %s", stderr)
	}
	var plan map[string]any
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("csv dry-run output not JSON: %v", err)
	}
	if rows, _ := plan["Rows"].([]any); len(rows) != 1 {
		t.Fatalf("csv plan rows = %v, want 1", plan["Rows"])
	}

	// Explicit mapping with non-standard headers.
	odd := writeImportFile(t, "odd.csv", "Full Name,Email Address\nJohn Roe,john@example.com\n")
	out, stderr, code = runCLI(t, "import", "csv", "--map", "name=Full Name,email=Email Address", "--dry-run", odd)
	if code != 0 {
		t.Fatalf("import csv mapped: code=%d stderr=%s", code, stderr)
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("mapped csv output not JSON: %v", err)
	}
	if rows, _ := plan["Rows"].([]any); len(rows) != 1 {
		t.Fatalf("mapped csv plan rows = %v, want 1", plan["Rows"])
	}
}

func TestImportCSV_RealImportCreatesContact(t *testing.T) {
	setTestProfileDirs(t)
	csvPath := writeImportFile(t, "c.csv", "name,email\nJane Doe,jane@example.com\n")
	_, stderr, code := runCLI(t, "import", "csv", csvPath)
	if code != 0 {
		t.Fatalf("import csv: code=%d stderr=%s", code, stderr)
	}
	out, _, _ := runCLI(t, "contact", "list", "--query", "Jane")
	if !strings.Contains(out, "Jane Doe") {
		t.Errorf("contact list after csv import missing Jane Doe: %s", out)
	}
}

func TestImport_ErrorPaths(t *testing.T) {
	setTestProfileDirs(t)

	if _, stderr, code := runCLI(t, "import"); code != 0 && !strings.Contains(stderr, "usage") {
		t.Errorf("import with no verb: code=%d stderr=%s, want usage error", code, stderr)
	}
	if _, stderr, code := runCLI(t, "import", "bogus"); code == 0 || !strings.Contains(stderr, "unknown subcommand") {
		t.Errorf("import bogus verb: code=%d stderr=%s", code, stderr)
	}
	if _, stderr, code := runCLI(t, "import", "vcard", filepath.Join(t.TempDir(), "missing.vcf")); code == 0 || !strings.Contains(stderr, "failed to open vCard") {
		t.Errorf("import missing file: code=%d stderr=%s", code, stderr)
	}
	// Invalid --resolve format.
	csvPath := writeImportFile(t, "c.csv", "name\nX\n")
	if _, stderr, code := runCLI(t, "import", "csv", "--resolve", "badformat", csvPath); code == 0 || !strings.Contains(stderr, "--resolve") {
		t.Errorf("import bad resolve: code=%d stderr=%s", code, stderr)
	}
	// Invalid --map field.
	if _, stderr, code := runCLI(t, "import", "csv", "--map", "bogus=Header", csvPath); code == 0 || !strings.Contains(stderr, "--map") {
		t.Errorf("import bad map: code=%d stderr=%s", code, stderr)
	}
}

// -- org ----------------------------------------------------------------

func TestOrgLifecycle_AllSubcommands(t *testing.T) {
	setTestProfileDirs(t)

	out, stderr, code := runCLI(t, "org", "add", "--name", "Acme Corp", "--notes", "widgets")
	if code != 0 {
		t.Fatalf("org add: code=%d stderr=%s", code, stderr)
	}
	var org map[string]any
	if err := json.Unmarshal([]byte(out), &org); err != nil {
		t.Fatalf("org add output not JSON: %v (%s)", err, out)
	}
	orgID, _ := org["ID"].(string)
	if orgID == "" {
		t.Fatalf("org add missing ID: %s", out)
	}

	// show
	out, _, _ = runCLI(t, "org", "show", orgID)
	if !strings.Contains(out, "Acme Corp") {
		t.Errorf("org show missing name: %s", out)
	}
	// list with query
	out, _, _ = runCLI(t, "org", "list", "--query", "acme")
	if !strings.Contains(out, "Acme Corp") {
		t.Errorf("org list missing org: %s", out)
	}
	// update
	out, _, _ = runCLI(t, "org", "update", "--name", "Acme Global", orgID)
	if !strings.Contains(out, "Acme Global") {
		t.Errorf("org update output: %s", out)
	}
	// tag + classify + add-point
	if _, stderr, code = runCLI(t, "org", "tag", orgID, "vendor"); code != 0 {
		t.Fatalf("org tag: code=%d stderr=%s", code, stderr)
	}
	if _, stderr, code = runCLI(t, "org", "classify", orgID, "business"); code != 0 {
		t.Fatalf("org classify: code=%d stderr=%s", code, stderr)
	}
	if _, stderr, code = runCLI(t, "org", "add-point", "--kind", "email", "--value", "info@acme.example", orgID); code != 0 {
		t.Fatalf("org add-point: code=%d stderr=%s", code, stderr)
	}
	out, _, _ = runCLI(t, "org", "show", orgID)
	if !strings.Contains(out, "info@acme.example") || !strings.Contains(out, "vendor") {
		t.Errorf("org show after enrichments missing data: %s", out)
	}
	// delete (plain, un-audited removal) — then show fails
	if out, stderr, code = runCLI(t, "org", "delete", orgID); code != 0 || !strings.Contains(out, "deleted") {
		t.Fatalf("org delete: code=%d out=%s stderr=%s", code, out, stderr)
	}
	if _, _, code = runCLI(t, "org", "show", orgID); code == 0 {
		t.Errorf("org show after delete succeeded, want error")
	}

	// erase (audited privacy path) on a second org — then show fails
	out, stderr, code = runCLI(t, "org", "add", "--name", "Erase Target")
	if code != 0 {
		t.Fatalf("org add (erase target): code=%d stderr=%s", code, stderr)
	}
	var eraseOrg map[string]any
	if err := json.Unmarshal([]byte(out), &eraseOrg); err != nil {
		t.Fatalf("org add output not JSON: %v", err)
	}
	eraseOrgID, _ := eraseOrg["ID"].(string)
	out, stderr, code = runCLI(t, "org", "erase", eraseOrgID)
	if code != 0 {
		t.Fatalf("org erase: code=%d stderr=%s", code, stderr)
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatalf("org erase output not JSON: %v", err)
	}
	if summary["EntityType"] != "organization" {
		t.Errorf("org erase summary = %v, want EntityType=organization", summary)
	}
	if _, _, code = runCLI(t, "org", "show", eraseOrgID); code == 0 {
		t.Errorf("org show after erase succeeded, want error")
	}
}

func TestOrg_UsageErrors(t *testing.T) {
	setTestProfileDirs(t)
	if _, _, code := runCLI(t, "org"); code == 0 {
		t.Error("org with no verb succeeded, want usage error")
	}
	if _, _, code := runCLI(t, "org", "nonsense"); code == 0 {
		t.Error("org unknown verb succeeded, want error")
	}
}

// -- followup ------------------------------------------------------------

func TestFollowUpLifecycle(t *testing.T) {
	setTestProfileDirs(t)

	_, _, code := runCLI(t, "contact", "add", "--name", "Alice")
	if code != 0 {
		t.Fatalf("contact add: code=%d", code)
	}
	out, _, _ := runCLI(t, "contact", "list", "--query", "Alice")
	var list struct {
		Items []struct {
			ID string `json:"ID"`
		} `json:"Items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list.Items) == 0 {
		t.Fatalf("contact list output: %v (%s)", err, out)
	}
	alice := list.Items[0].ID

	due := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	out, stderr, code := runCLI(t, "followup", "add", "--person", alice, "--due", due, "--notes", "check in")
	if code != 0 {
		t.Fatalf("followup add: code=%d stderr=%s", code, stderr)
	}
	var fu map[string]any
	if err := json.Unmarshal([]byte(out), &fu); err != nil {
		t.Fatalf("followup add output not JSON: %v", err)
	}
	fuID, _ := fu["ID"].(string)

	// list
	out, _, _ = runCLI(t, "followup", "list", "--person", alice)
	if !strings.Contains(out, fuID) {
		t.Errorf("followup list missing id: %s", out)
	}
	// complete
	out, _, _ = runCLI(t, "followup", "complete", fuID)
	if !strings.Contains(out, "completed") {
		t.Errorf("followup complete output: %s", out)
	}
	// complete again -> conflict (error)
	if _, _, code = runCLI(t, "followup", "complete", fuID); code == 0 {
		t.Errorf("second followup complete succeeded, want conflict")
	}
	// cancel a fresh one
	out, _, _ = runCLI(t, "followup", "add", "--person", alice, "--due", due)
	var fu2 map[string]any
	_ = json.Unmarshal([]byte(out), &fu2)
	fu2ID, _ := fu2["ID"].(string)
	if _, _, code = runCLI(t, "followup", "cancel", fu2ID); code != 0 {
		t.Errorf("followup cancel failed: code=%d", code)
	}
}

func TestFollowUp_ValidationErrors(t *testing.T) {
	setTestProfileDirs(t)
	due := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	if _, _, code := runCLI(t, "followup", "add", "--due", due); code == 0 {
		t.Error("followup add with neither person nor org succeeded, want error")
	}
	if _, _, code := runCLI(t, "followup", "add", "--person", "x", "--due", "not-a-date"); code == 0 {
		t.Error("followup add with bad due date succeeded, want error")
	}
	if _, _, code := runCLI(t, "followup", "list"); code == 0 {
		t.Error("followup list with no entity succeeded, want error")
	}
}

// -- interaction ---------------------------------------------------------

func TestInteractionLifecycle(t *testing.T) {
	setTestProfileDirs(t)

	_, _, _ = runCLI(t, "contact", "add", "--name", "Alice")
	out, _, _ := runCLI(t, "contact", "list", "--query", "Alice")
	var list struct {
		Items []struct {
			ID string `json:"ID"`
		} `json:"Items"`
	}
	_ = json.Unmarshal([]byte(out), &list)
	alice := list.Items[0].ID

	occurred := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	out, stderr, code := runCLI(t, "interaction", "add", "--person", alice, "--kind", "call", "--summary", "intro call", "--occurred", occurred)
	if code != 0 {
		t.Fatalf("interaction add: code=%d stderr=%s", code, stderr)
	}
	var in map[string]any
	if err := json.Unmarshal([]byte(out), &in); err != nil {
		t.Fatalf("interaction add output not JSON: %v", err)
	}
	if in["Summary"] != "intro call" {
		t.Errorf("interaction add = %v, want summary intro call", in)
	}

	out, _, _ = runCLI(t, "interaction", "list", "--person", alice)
	if !strings.Contains(out, "intro call") {
		t.Errorf("interaction list missing summary: %s", out)
	}
	out, _, _ = runCLI(t, "interaction", "last", "--person", alice)
	if !strings.Contains(out, "intro call") {
		t.Errorf("interaction last output: %s", out)
	}
}

func TestInteraction_ValidationErrors(t *testing.T) {
	setTestProfileDirs(t)
	// No entity.
	if _, _, code := runCLI(t, "interaction", "add", "--kind", "note", "--summary", "x"); code == 0 {
		t.Error("interaction add with no entity succeeded, want error")
	}
	// Bad occurred.
	if _, _, code := runCLI(t, "interaction", "add", "--person", "x", "--kind", "note", "--summary", "x", "--occurred", "nope"); code == 0 {
		t.Error("interaction add with bad occurred succeeded, want error")
	}
	// Unknown subcommand.
	if _, _, code := runCLI(t, "interaction", "bogus"); code == 0 {
		t.Error("interaction unknown verb succeeded, want error")
	}
}

// -- relate --------------------------------------------------------------

func TestRelate_AddAndList(t *testing.T) {
	setTestProfileDirs(t)

	_, _, _ = runCLI(t, "contact", "add", "--name", "Alice")
	_, _, _ = runCLI(t, "contact", "add", "--name", "Bob")
	out, _, _ := runCLI(t, "contact", "list")
	var list struct {
		Items []struct {
			ID string `json:"ID"`
		} `json:"Items"`
	}
	_ = json.Unmarshal([]byte(out), &list)
	var alice, bob string
	for _, it := range list.Items {
		// Identify by re-querying names is not possible here; use show on both ids.
		if alice == "" {
			alice = it.ID
		} else {
			bob = it.ID
		}
	}

	out, stderr, code := runCLI(t, "relate", "add", "--from", alice, "--to-person", bob, "--type", "friend")
	if code != 0 {
		t.Fatalf("relate add: code=%d stderr=%s", code, stderr)
	}
	var rel map[string]any
	if err := json.Unmarshal([]byte(out), &rel); err != nil {
		t.Fatalf("relate add output not JSON: %v", err)
	}
	if rel["Type"] != "friend" {
		t.Errorf("relate add = %v, want type friend", rel)
	}

	out, _, _ = runCLI(t, "relate", "outgoing", alice)
	if !strings.Contains(out, "friend") {
		t.Errorf("relate outgoing missing relationship: %s", out)
	}
	out, _, _ = runCLI(t, "relate", "incoming-person", bob)
	if !strings.Contains(out, "friend") {
		t.Errorf("relate incoming-person missing relationship: %s", out)
	}
	// No org incoming (empty list is valid JSON).
	if _, _, code = runCLI(t, "relate", "incoming-org", "does-not-exist"); code != 0 {
		t.Errorf("relate incoming-org on unknown id: code=%d, want 0 (empty list)", code)
	}
}

// -- memory --------------------------------------------------------------

func TestMemory_StatusAndLocalLinkFlow(t *testing.T) {
	setTestProfileDirs(t)

	// status always succeeds — SymMemory is optional (graceful fallback).
	out, stderr, code := runCLI(t, "memory", "status")
	if code != 0 {
		t.Fatalf("memory status: code=%d stderr=%s", code, stderr)
	}
	var status map[string]any
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("memory status output not JSON: %v", err)
	}
	if _, ok := status["available"]; !ok {
		t.Errorf("memory status missing available key: %s", out)
	}

	// link/unlink/show work purely locally.
	_, _, _ = runCLI(t, "contact", "add", "--name", "Alice")
	out, _, _ = runCLI(t, "contact", "list", "--query", "Alice")
	var list struct {
		Items []struct {
			ID string `json:"ID"`
		} `json:"Items"`
	}
	_ = json.Unmarshal([]byte(out), &list)
	if len(list.Items) == 0 {
		t.Fatal("no contact created")
	}
	alice := list.Items[0].ID

	_, stderr, code = runCLI(t, "memory", "link", "--person", alice, "--entity", "mem-entity-1")
	if code != 0 {
		t.Fatalf("memory link: code=%d stderr=%s", code, stderr)
	}
	out, _, _ = runCLI(t, "memory", "show", "--person", alice)
	if !strings.Contains(out, "linked") || !strings.Contains(out, "mem-entity-1") {
		t.Errorf("memory show output: %s", out)
	}
	// Second link is a conflict.
	if _, _, code = runCLI(t, "memory", "link", "--person", alice, "--entity", "mem-entity-2"); code == 0 {
		t.Errorf("second memory link succeeded, want conflict")
	}
	if _, stderr, code = runCLI(t, "memory", "unlink", "--person", alice); code != 0 {
		t.Fatalf("memory unlink: code=%d stderr=%s", code, stderr)
	}
	out, _, _ = runCLI(t, "memory", "show", "--person", alice)
	if !strings.Contains(out, "false") {
		t.Errorf("memory show after unlink: %s", out)
	}
}

func TestMemory_ValidationErrors(t *testing.T) {
	setTestProfileDirs(t)
	if _, _, code := runCLI(t, "memory", "link", "--person", "x"); code == 0 {
		t.Error("memory link without entity succeeded, want error")
	}
	if _, _, code := runCLI(t, "memory", "link", "--entity", "e"); code == 0 {
		t.Error("memory link without person/org succeeded, want error")
	}
	if _, _, code := runCLI(t, "memory", "relate", "--person", "x", "--relation", "r"); code == 0 {
		t.Error("memory relate without to-id succeeded, want error")
	}
}

// -- meeting --------------------------------------------------------------

func TestMeeting_StatusAndImport(t *testing.T) {
	setTestProfileDirs(t)

	// status always succeeds — SymMeet is optional (graceful fallback).
	out, stderr, code := runCLI(t, "meeting", "status")
	if code != 0 {
		t.Fatalf("meeting status: code=%d stderr=%s", code, stderr)
	}
	var status map[string]any
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("meeting status output not JSON: %v", err)
	}
	if _, ok := status["available"]; !ok {
		t.Errorf("meeting status missing available key: %s", out)
	}

	// Import from a fixture manifest into a person.
	_, _, _ = runCLI(t, "contact", "add", "--name", "Alice")
	out, _, _ = runCLI(t, "contact", "list", "--query", "Alice")
	var list struct {
		Items []struct {
			ID string `json:"ID"`
		} `json:"Items"`
	}
	_ = json.Unmarshal([]byte(out), &list)
	if len(list.Items) == 0 {
		t.Fatal("no contact created")
	}
	alice := list.Items[0].ID

	fixtureDir := writeManifest(t, `{
		"schema_version": 1,
		"meeting_id": "meeting-import-001",
		"source": "imported",
		"created_at": "2026-01-15T10:00:00Z",
		"consent": {"status": "required"},
		"retention": {"policy": "keep"}
	}`)

	out, stderr, code = runCLI(t, "meeting", "import", "--fixture", fixtureDir, "--person", alice)
	if code != 0 {
		t.Fatalf("meeting import: code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "source=imported consent=required retention=keep") {
		t.Errorf("meeting import stderr missing status line: %s", stderr)
	}
	var results []map[string]any
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("meeting import output not JSON: %v (%s)", err, out)
	}
	if len(results) != 1 || results[0]["created"] != true {
		t.Errorf("meeting import results = %v, want 1 created outcome", results)
	}

	// Re-import of the same meeting id is idempotent (created=false).
	out, _, code = runCLI(t, "meeting", "import", "--fixture", fixtureDir, "--person", alice)
	if code != 0 {
		t.Fatalf("meeting re-import: code=%d", code)
	}
	_ = json.Unmarshal([]byte(out), &results)
	if len(results) != 1 || results[0]["created"] != false {
		t.Errorf("meeting re-import results = %v, want created=false (idempotent)", results)
	}

	// Interaction appears in the timeline.
	out, _, _ = runCLI(t, "interaction", "list", "--person", alice)
	if !strings.Contains(out, "meeting-import-001") {
		t.Errorf("interaction list missing meeting id: %s", out)
	}
}

func TestMeeting_ValidationErrors(t *testing.T) {
	setTestProfileDirs(t)

	if _, _, code := runCLI(t, "meeting", "bogus"); code == 0 {
		t.Error("meeting unknown verb succeeded, want error")
	}
	// No id and no fixture.
	if _, _, code := runCLI(t, "meeting", "import", "--person", "x"); code == 0 {
		t.Error("meeting import without id succeeded, want error")
	}
	// No person/org.
	if _, _, code := runCLI(t, "meeting", "import", "--id", "m1"); code == 0 {
		t.Error("meeting import without person/org succeeded, want error")
	}
	// Missing fixture manifest.
	if _, _, code := runCLI(t, "meeting", "import", "--fixture", t.TempDir(), "--person", "x"); code == 0 {
		t.Error("meeting import with missing fixture succeeded, want error")
	}
}

// -- human formatters -----------------------------------------------------

func TestHumanize_AllRenderers(t *testing.T) {
	now := time.Now()

	// Person.
	p := &contact.Person{ID: "p1", DisplayName: "Ada Lovelace", Tags: []string{"vip"}, Classifications: []contact.Classification{contact.ClassificationBusiness}, Notes: "note"}
	p.ContactPoints = append(p.ContactPoints, contact.ContactPoint{Kind: contact.ContactPointEmail, RawValue: "ada@example.com", IsPreferred: true})
	hp := humanPerson(p)
	for _, want := range []string{"Ada Lovelace", "vip", "business", "ada@example.com", "preferred"} {
		if !strings.Contains(hp, want) {
			t.Errorf("humanPerson missing %q: %s", want, hp)
		}
	}

	// Organization.
	ho := humanOrganization(&contact.Organization{ID: "o1", Name: "Acme", Notes: "n"})
	if !strings.Contains(ho, "Acme") || !strings.Contains(ho, "n") {
		t.Errorf("humanOrganization: %s", ho)
	}

	// Membership with period.
	from := now.AddDate(0, -6, 0)
	to := now.AddDate(0, 6, 0)
	hm := humanMembership(contact.Membership{ID: "m1", PersonID: "p1", OrganizationID: "o1", Role: "employee", Title: "Engineer", ValidFrom: &from, ValidTo: &to})
	for _, want := range []string{"employee", "Engineer", "to"} {
		if !strings.Contains(hm, want) {
			t.Errorf("humanMembership missing %q: %s", want, hm)
		}
	}

	// Relationship (person target and org target).
	hr := humanRelationship(relationship.Relationship{ID: "r1", FromPersonID: "p1", ToPersonID: "p2", Type: "friend"})
	if !strings.Contains(hr, "friend") || !strings.Contains(hr, "person") {
		t.Errorf("humanRelationship person-target: %s", hr)
	}
	hr2 := humanRelationship(relationship.Relationship{ID: "r2", FromPersonID: "p1", ToOrganizationID: "o1", Type: "vendor-contact"})
	if !strings.Contains(hr2, "organization") {
		t.Errorf("humanRelationship org-target: %s", hr2)
	}

	// Interaction with external ref.
	hi := humanInteraction(relationship.Interaction{ID: "i1", Kind: relationship.InteractionEmail, OccurredAt: now, Summary: "hello", ExternalRef: "ref-1"})
	if !strings.Contains(hi, "hello") || !strings.Contains(hi, "ref-1") {
		t.Errorf("humanInteraction: %s", hi)
	}

	// Follow-up.
	hf := humanFollowUp(relationship.FollowUp{ID: "f1", DueAt: now, Status: relationship.FollowUpOpen, Notes: "call back"})
	if !strings.Contains(hf, "call back") || !strings.Contains(hf, "open") {
		t.Errorf("humanFollowUp: %s", hf)
	}

	// Timeline entries (both kinds).
	entries := []relationship.TimelineEntry{
		{Kind: relationship.TimelineInteraction, At: now, Interaction: &relationship.Interaction{ID: "i1", Summary: "m"}},
		{Kind: relationship.TimelineFollowUp, At: now, FollowUp: &relationship.FollowUp{ID: "f1", Notes: "n"}},
	}
	ht := humanTimelineEntry(entries[0])
	if !strings.Contains(ht, "m") {
		t.Errorf("humanTimelineEntry interaction: %s", ht)
	}
	ht = humanTimelineEntry(entries[1])
	if !strings.Contains(ht, "n") {
		t.Errorf("humanTimelineEntry followup: %s", ht)
	}

	// humanList plural/singular/empty.
	if got := humanList(0, "person", "persons", func(int) string { return "" }); got != "no persons" {
		t.Errorf("humanList(0) = %q, want no persons", got)
	}
	if got := humanList(1, "person", "persons", func(int) string { return "x" }); !strings.Contains(got, "1 person") {
		t.Errorf("humanList(1) = %q, want singular", got)
	}
	if got := humanList(2, "person", "persons", func(int) string { return "x" }); !strings.Contains(got, "2 persons") {
		t.Errorf("humanList(2) = %q, want plural", got)
	}

	// humanize dispatch through --human paths.
	if got := humanize(&contact.Person{DisplayName: "X"}); !strings.Contains(got, "X") {
		t.Errorf("humanize person: %s", got)
	}
	if got := humanize(&contact.Ref{DisplayName: "R", Kind: "person", Provider: "relate", ID: "p1", SchemaVersion: 1}); !strings.Contains(got, "[ref relate:p1 schema v1]") {
		t.Errorf("humanize ref: %s", got)
	}
	if got := humanize(struct{ A int }{A: 1}); !strings.Contains(got, `"A": 1`) {
		t.Errorf("humanize fallback JSON: %s", got)
	}
	if got := humanMore(true); !strings.Contains(got, "more results") {
		t.Errorf("humanMore(true): %s", got)
	}
	if got := humanMore(false); got != "" {
		t.Errorf("humanMore(false) = %q, want empty", got)
	}

	// List-dispatch paths (page.Result and typed slices) — these exercise
	// humanPersonLine/humanOrganizationLine/humanContactPoint.
	personPage := humanize(page.Result[contact.Person]{Items: []contact.Person{{ID: "p1", DisplayName: "Ada"}}})
	if !strings.Contains(personPage, "1 person") || !strings.Contains(personPage, "Ada (p1)") {
		t.Errorf("humanize person list: %s", personPage)
	}
	orgPage := humanize(page.Result[contact.Organization]{Items: []contact.Organization{{ID: "o1", Name: "Acme"}}})
	if !strings.Contains(orgPage, "1 organization") || !strings.Contains(orgPage, "Acme (o1)") {
		t.Errorf("humanize org list: %s", orgPage)
	}
	cp := humanize(&contact.ContactPoint{ID: "c1", Kind: contact.ContactPointEmail, RawValue: "a@b.c", Label: "work", IsPreferred: true})
	if !strings.Contains(cp, "a@b.c") || !strings.Contains(cp, "[work]") || !strings.Contains(cp, "preferred") || !strings.Contains(cp, "(c1)") {
		t.Errorf("humanize contact point: %s", cp)
	}
	memberships := humanize([]contact.Membership{{ID: "m1", PersonID: "p1", OrganizationID: "o1", Role: "employee"}})
	if !strings.Contains(memberships, "1 membership") || !strings.Contains(memberships, "employee") {
		t.Errorf("humanize memberships: %s", memberships)
	}
	rels := humanize([]relationship.Relationship{{ID: "r1", FromPersonID: "p1", ToPersonID: "p2", Type: "friend"}})
	if !strings.Contains(rels, "1 relationship") || !strings.Contains(rels, "friend") {
		t.Errorf("humanize relationships: %s", rels)
	}
	interactions := humanize([]relationship.Interaction{{ID: "i1", Kind: relationship.InteractionNote, Summary: "s"}})
	if !strings.Contains(interactions, "1 interaction") {
		t.Errorf("humanize interactions: %s", interactions)
	}
	followUps := humanize([]relationship.FollowUp{{ID: "f1", Notes: "n"}})
	if !strings.Contains(followUps, "1 follow-up") {
		t.Errorf("humanize follow-ups: %s", followUps)
	}
	timeline := humanize([]relationship.TimelineEntry{{Kind: relationship.TimelineInteraction, At: now, Interaction: &relationship.Interaction{ID: "i1", Summary: "m"}}})
	if !strings.Contains(timeline, "1 entry") {
		t.Errorf("humanize timeline entries: %s", timeline)
	}
	emptyList := humanize([]relationship.FollowUp{})
	if !strings.Contains(emptyList, "no follow-ups") {
		t.Errorf("humanize empty list: %s", emptyList)
	}
}

// -- console + init --------------------------------------------------------

func TestConsole_TokenFileWritten0600(t *testing.T) {
	setTestProfileDirs(t)
	dataDir := os.Getenv("SYMRELATE_DATA_HOME")

	if err := writeConsoleTokenFile(dataDir, "sometoken"); err != nil {
		t.Fatalf("writeConsoleTokenFile() error = %v", err)
	}
	path := filepath.Join(dataDir, "console-token")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if !strings.Contains(string(b), "sometoken") {
		t.Errorf("token file content = %q, want sometoken", string(b))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestRunInit_PrintsDirs(t *testing.T) {
	setTestProfileDirs(t)
	out, stderr, code := runCLI(t, "init")
	if code != 0 {
		t.Fatalf("init: code=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{"config dir:", "data dir:", "cache dir:", "database:"} {
		if !strings.Contains(out, want) {
			t.Errorf("init output missing %q: %s", want, out)
		}
	}
}

// TestRunConsole_ServesAndStops tests the console command's run loop with
// a cancellable context: it must bind, write the token file, and return
// promptly when the context is cancelled. Serve returns the context error
// on shutdown, which the CLI maps to exit code 1 — that is the existing
// console contract (the process exits when its context is cancelled).
func TestRunConsole_ServesAndStops(t *testing.T) {
	setTestProfileDirs(t)

	ctx, cancel := context.WithCancel(context.Background())
	var stderr strings.Builder
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, IO{Stdout: &strings.Builder{}, Stderr: &stderr}, []string{"console"})
	}()

	// Wait for the listener + token file, then cancel.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(os.Getenv("SYMRELATE_DATA_HOME"), "console-token")); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case code := <-done:
		if code != 1 {
			t.Errorf("console run returned code %d, want 1 (context-cancelled serve)", code)
		}
		if !strings.Contains(stderr.String(), "symrelate console: listening on") {
			t.Errorf("console stderr missing startup line: %s", stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("console run did not stop after context cancellation")
	}
}
