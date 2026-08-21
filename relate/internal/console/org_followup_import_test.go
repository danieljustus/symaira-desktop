package console

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestHTTPServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	s, token := testServer(t)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, token
}

// -- organizations -------------------------------------------------------

func TestOrganizationsLifecycle_ListCreateGetUpdateErase(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	// Empty list first.
	resp, listed := doJSON(t, srv, token, http.MethodGet, "/api/v1/organizations", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, body = %v", resp.StatusCode, listed)
	}

	resp, created := doJSON(t, srv, token, http.MethodPost, "/api/v1/organizations", map[string]any{
		"name": "Acme Corp", "notes": "widgets",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body = %v", resp.StatusCode, created)
	}
	id, _ := created["ID"].(string)
	if id == "" {
		t.Fatalf("created organization missing ID: %v", created)
	}

	resp, got := doJSON(t, srv, token, http.MethodGet, "/api/v1/organizations/"+id, nil)
	if resp.StatusCode != http.StatusOK || got["Name"] != "Acme Corp" {
		t.Fatalf("get status=%d body=%v", resp.StatusCode, got)
	}

	resp, updated := doJSON(t, srv, token, http.MethodPatch, "/api/v1/organizations/"+id, map[string]any{"name": "Acme Global"})
	if resp.StatusCode != http.StatusOK || updated["Name"] != "Acme Global" {
		t.Fatalf("update status=%d body=%v", resp.StatusCode, updated)
	}

	resp, erased := doJSON(t, srv, token, http.MethodDelete, "/api/v1/organizations/"+id, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("erase status = %d, body = %v", resp.StatusCode, erased)
	}

	resp, _ = doJSON(t, srv, token, http.MethodGet, "/api/v1/organizations/"+id, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after erase status = %d, want 404", resp.StatusCode)
	}
}

func TestOrganizationCreate_RejectsEmptyName(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	resp, body := doJSON(t, srv, token, http.MethodPost, "/api/v1/organizations", map[string]any{"name": "  "})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400, body = %v", resp.StatusCode, body)
	}
}

func TestOrganizationErase_UnknownID_IsNotFound(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	resp, _ := doJSON(t, srv, token, http.MethodDelete, "/api/v1/organizations/does-not-exist", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestOrganizationTimelineAndMemberships(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	resp, created := doJSON(t, srv, token, http.MethodPost, "/api/v1/organizations", map[string]any{"name": "Acme"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	orgID, _ := created["ID"].(string)

	// Timeline on a fresh org is an empty array.
	resp, tl := doJSONAny(t, srv, token, http.MethodGet, "/api/v1/organizations/"+orgID+"/timeline")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("timeline status = %d, body = %v", resp.StatusCode, tl)
	}
	if arr, ok := tl.([]any); !ok || len(arr) != 0 {
		t.Errorf("timeline = %v, want empty array", tl)
	}

	// Memberships on a fresh org is an empty array.
	resp, ms := doJSONAny(t, srv, token, http.MethodGet, "/api/v1/organizations/"+orgID+"/memberships")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("memberships status = %d, body = %v", resp.StatusCode, ms)
	}
	if arr, ok := ms.([]any); ok && len(arr) != 0 {
		t.Errorf("memberships = %v, want empty", ms)
	}

	// A person linked via membership appears in the org's memberships.
	resp, person := doJSON(t, srv, token, http.MethodPost, "/api/v1/contacts", map[string]any{"display_name": "Alice"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("contact create status = %d", resp.StatusCode)
	}
	personID, _ := person["ID"].(string)

	resp, mem := doJSON(t, srv, token, http.MethodPost, "/api/v1/memberships", map[string]any{
		"person_id": personID, "organization_id": orgID, "role": "employee",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("membership create status = %d, body = %v", resp.StatusCode, mem)
	}

	resp, ms = doJSONAny(t, srv, token, http.MethodGet, "/api/v1/organizations/"+orgID+"/memberships")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("memberships status = %d", resp.StatusCode)
	}
	if arr, ok := ms.([]any); !ok || len(arr) != 1 {
		t.Errorf("memberships = %v, want 1 item", ms)
	}
}

func TestMembershipCreate_RequiresBothIDs(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	resp, body := doJSON(t, srv, token, http.MethodPost, "/api/v1/memberships", map[string]any{"person_id": "x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400, body = %v", resp.StatusCode, body)
	}
}

func TestMembershipCreate_UnknownEntity_IsBadRequest(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	resp, _ := doJSON(t, srv, token, http.MethodPost, "/api/v1/memberships", map[string]any{
		"person_id": "does-not-exist", "organization_id": "also-missing",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// -- follow-ups ----------------------------------------------------------

func TestContactTimelineAndMemberships(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	resp, person := doJSON(t, srv, token, http.MethodPost, "/api/v1/contacts", map[string]any{"display_name": "Ada Lovelace"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("contact create status = %d", resp.StatusCode)
	}
	personID, _ := person["ID"].(string)

	// Timeline with an interaction and a follow-up.
	dueAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	resp, fu := doJSON(t, srv, token, http.MethodPost, "/api/v1/followups", map[string]any{
		"person_id": personID, "due_at": dueAt, "notes": "call",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("followup create status = %d, body = %v", resp.StatusCode, fu)
	}

	resp, tl := doJSONAny(t, srv, token, http.MethodGet, "/api/v1/contacts/"+personID+"/timeline")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("timeline status = %d, body = %v", resp.StatusCode, tl)
	}
	if arr, ok := tl.([]any); !ok || len(arr) != 1 {
		t.Errorf("timeline = %v, want 1 entry", tl)
	}

	// Memberships: none initially, then one after linking an org.
	resp, ms := doJSONAny(t, srv, token, http.MethodGet, "/api/v1/contacts/"+personID+"/memberships")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("memberships status = %d", resp.StatusCode)
	}
	if arr, ok := ms.([]any); ok && len(arr) != 0 {
		t.Errorf("memberships = %v, want empty", ms)
	}

	resp, org := doJSON(t, srv, token, http.MethodPost, "/api/v1/organizations", map[string]any{"name": "Acme"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("org create status = %d", resp.StatusCode)
	}
	orgID, _ := org["ID"].(string)
	resp, mem := doJSON(t, srv, token, http.MethodPost, "/api/v1/memberships", map[string]any{
		"person_id": personID, "organization_id": orgID, "role": "employee",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("membership create status = %d, body = %v", resp.StatusCode, mem)
	}

	resp, ms = doJSONAny(t, srv, token, http.MethodGet, "/api/v1/contacts/"+personID+"/memberships")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("memberships status = %d", resp.StatusCode)
	}
	if arr, ok := ms.([]any); !ok || len(arr) != 1 {
		t.Errorf("memberships = %v, want 1 item", ms)
	}
}

func TestFollowUpsLifecycle_CreateListCompleteCancel(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	resp, org := doJSON(t, srv, token, http.MethodPost, "/api/v1/organizations", map[string]any{"name": "Acme"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("org create status = %d", resp.StatusCode)
	}
	orgID, _ := org["ID"].(string)

	dueAt := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	resp, created := doJSON(t, srv, token, http.MethodPost, "/api/v1/followups", map[string]any{
		"organization_id": orgID, "due_at": dueAt, "notes": "check in",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("followup create status = %d, body = %v", resp.StatusCode, created)
	}
	fuID, _ := created["ID"].(string)
	if fuID == "" {
		t.Fatalf("created follow-up missing ID: %v", created)
	}

	// List by organization shows it.
	resp, listed := doJSONAny(t, srv, token, http.MethodGet, "/api/v1/followups?organization_id="+orgID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	if arr, ok := listed.([]any); !ok || len(arr) != 1 {
		t.Errorf("listed = %v, want 1 follow-up", listed)
	}

	// Complete it.
	resp, done := doJSON(t, srv, token, http.MethodPost, "/api/v1/followups/"+fuID+"/complete", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete status = %d, body = %v", resp.StatusCode, done)
	}

	// Completing again is a conflict.
	resp, _ = doJSON(t, srv, token, http.MethodPost, "/api/v1/followups/"+fuID+"/complete", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("second complete status = %d, want 409", resp.StatusCode)
	}

	// Cancel a fresh one.
	resp, created2 := doJSON(t, srv, token, http.MethodPost, "/api/v1/followups", map[string]any{
		"organization_id": orgID, "due_at": dueAt,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("followup2 create status = %d", resp.StatusCode)
	}
	fu2ID, _ := created2["ID"].(string)
	resp, cancelled := doJSON(t, srv, token, http.MethodPost, "/api/v1/followups/"+fu2ID+"/cancel", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %v", resp.StatusCode, cancelled)
	}
}

func TestFollowUpsList_RequiresExactlyOneEntity(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	// Neither person_id nor organization_id.
	resp, body := doJSON(t, srv, token, http.MethodGet, "/api/v1/followups", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no-entity status = %d, want 400, body = %v", resp.StatusCode, body)
	}
	// Both set.
	resp, _ = doJSON(t, srv, token, http.MethodGet, "/api/v1/followups?person_id=a&organization_id=b", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("both-entities status = %d, want 400", resp.StatusCode)
	}
}

func TestFollowUpCreate_RejectsMissingDueDate(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	resp, org := doJSON(t, srv, token, http.MethodPost, "/api/v1/organizations", map[string]any{"name": "Acme"})
	orgID, _ := org["ID"].(string)

	resp, body := doJSON(t, srv, token, http.MethodPost, "/api/v1/followups", map[string]any{
		"organization_id": orgID, "due_at": "not-a-timestamp",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad due_at status = %d, want 400, body = %v", resp.StatusCode, body)
	}
}

func TestFollowUpCreate_UnknownOrganization_IsBadRequest(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	dueAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	resp, _ := doJSON(t, srv, token, http.MethodPost, "/api/v1/followups", map[string]any{
		"organization_id": "does-not-exist", "due_at": dueAt,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// -- import --------------------------------------------------------------

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

// doJSONAny is doJSON (server_test.go) for endpoints whose response is a
// JSON array: it decodes the body into any so callers can assert on the
// array shape.
func doJSONAny(t *testing.T, srv *httptest.Server, token, method, path string) (*http.Response, any) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var parsed any
	dec := json.NewDecoder(resp.Body)
	_ = dec.Decode(&parsed)
	return resp, parsed
}

const importVCard = "BEGIN:VCARD\r\n" +
	"VERSION:3.0\r\n" +
	"FN:Jordan Example\r\n" +
	"N:Example;Jordan;;;\r\n" +
	"EMAIL:jordan@example.com\r\n" +
	"TEL:+15550000001\r\n" +
	"UID:console-import-vcard-001\r\n" +
	"END:VCARD\r\n"

func TestServe_ServesRequestsUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s, token := testServer(t)
	ln, err := Listen(0)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()

	// The server must answer an authenticated request on the real
	// listener before shutdown.
	addr := "http://" + ln.Addr().String()
	req, err := http.NewRequest(http.MethodGet, addr+"/api/v1/contacts", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET on real listener: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Cancelling the context shuts the server down gracefully.
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Serve() returned %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve() did not return after context cancellation")
	}
}
func TestImportPlan_VCard(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	vcardPath := writeTempFile(t, "contact.vcf", importVCard)

	resp, plan := doJSON(t, srv, token, http.MethodPost, "/api/v1/import/plan", map[string]any{
		"path": vcardPath, "kind": "vcard",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plan status = %d, body = %v", resp.StatusCode, plan)
	}
	rows, ok := plan["Rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("plan rows = %v, want 1 row", plan["Rows"])
	}
}

func TestImportPlan_CSVWithDetectedMapping(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	csvPath := writeTempFile(t, "contacts.csv", "name,email\nJane Doe,jane@example.com\n")

	resp, plan := doJSON(t, srv, token, http.MethodPost, "/api/v1/import/plan", map[string]any{
		"path": csvPath, "kind": "csv",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plan status = %d, body = %v", resp.StatusCode, plan)
	}
	rows, ok := plan["Rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("plan rows = %v, want 1 row", plan["Rows"])
	}
}

func TestImportPlan_CSVWithExplicitMapping(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	csvPath := writeTempFile(t, "contacts.csv", "Full Name,Email Address\nJohn Roe,john@example.com\n")

	// Non-standard headers: auto-detection cannot map them, so the caller
	// provides an explicit column map.
	resp, plan := doJSON(t, srv, token, http.MethodPost, "/api/v1/import/plan", map[string]any{
		"path": csvPath, "kind": "csv",
		"map": map[string]string{"name": "Full Name", "email": "Email Address"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plan status = %d, body = %v", resp.StatusCode, plan)
	}
	rows, ok := plan["Rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("plan rows = %v, want 1 row", plan["Rows"])
	}
}

func TestImportPlan_InvalidKind_IsBadRequest(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	resp, body := doJSON(t, srv, token, http.MethodPost, "/api/v1/import/plan", map[string]any{
		"path": "/nonexistent/file", "kind": "xml",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400, body = %v", resp.StatusCode, body)
	}
}

func TestImportPlan_MissingFile_IsBadRequest(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	resp, body := doJSON(t, srv, token, http.MethodPost, "/api/v1/import/plan", map[string]any{
		"path": filepath.Join(t.TempDir(), "missing.vcf"), "kind": "vcard",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400, body = %v", resp.StatusCode, body)
	}
}

func TestImportPlan_EmptyCSV_IsBadRequest(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	csvPath := writeTempFile(t, "empty.csv", "")
	resp, body := doJSON(t, srv, token, http.MethodPost, "/api/v1/import/plan", map[string]any{
		"path": csvPath, "kind": "csv",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400, body = %v", resp.StatusCode, body)
	}
}

func TestImportApply_VCardCreatesContact(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	vcardPath := writeTempFile(t, "contact.vcf", importVCard)

	// Plan first.
	resp, plan := doJSON(t, srv, token, http.MethodPost, "/api/v1/import/plan", map[string]any{
		"path": vcardPath, "kind": "vcard",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plan status = %d, body = %v", resp.StatusCode, plan)
	}

	// Apply with no resolutions (no duplicate candidates for a fresh UID).
	resp, applied := doJSON(t, srv, token, http.MethodPost, "/api/v1/import/apply", map[string]any{
		"path": vcardPath, "kind": "vcard",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d, body = %v", resp.StatusCode, applied)
	}

	// The contact now exists via the contacts API.
	resp, listed := doJSON(t, srv, token, http.MethodGet, "/api/v1/contacts?q=Jordan", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	if !strings.Contains(fmt.Sprintf("%v", listed), "Jordan Example") {
		t.Errorf("imported contact not visible in list: %v", listed)
	}

	// Import runs record the run.
	resp, runs := doJSONAny(t, srv, token, http.MethodGet, "/api/v1/import/runs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("runs status = %d", resp.StatusCode)
	}
	if arr, ok := runs.([]any); !ok || len(arr) == 0 {
		t.Errorf("import runs = %v, want at least 1 run", runs)
	}
}

func TestImportApply_DuplicateResolution(t *testing.T) {
	srv, token := newTestHTTPServer(t)

	// Import the same vCard twice; the second plan must flag the source
	// as an exact re-match.
	vcardPath := writeTempFile(t, "contact.vcf", importVCard)

	resp, _ := doJSON(t, srv, token, http.MethodPost, "/api/v1/import/apply", map[string]any{
		"path": vcardPath, "kind": "vcard",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first apply status = %d", resp.StatusCode)
	}

	resp, plan := doJSON(t, srv, token, http.MethodPost, "/api/v1/import/plan", map[string]any{
		"path": vcardPath, "kind": "vcard",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second plan status = %d", resp.StatusCode)
	}
	dups, ok := plan["Duplicates"].([]any)
	if !ok || len(dups) == 0 {
		t.Fatalf("second plan duplicates = %v, want >= 1 candidate", plan["Duplicates"])
	}

	// Resolve with skip and apply — no second contact is created.
	resp, applied := doJSON(t, srv, token, http.MethodPost, "/api/v1/import/apply", map[string]any{
		"path": vcardPath, "kind": "vcard",
		"resolutions": []map[string]any{{"RowNumber": 1, "Resolution": "skip"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply with resolution status = %d, body = %v", resp.StatusCode, applied)
	}
}
