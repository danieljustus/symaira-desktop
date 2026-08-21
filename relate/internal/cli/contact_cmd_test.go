package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func setTestProfileDirs(t *testing.T) {
	t.Helper()
	t.Setenv("SYMRELATE_CONFIG_HOME", t.TempDir())
	t.Setenv("SYMRELATE_DATA_HOME", t.TempDir())
	t.Setenv("SYMRELATE_CACHE_HOME", t.TempDir())
}

func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = Run(context.Background(), IO{Stdout: &out, Stderr: &errBuf}, args)
	return out.String(), errBuf.String(), code
}

// TestContactLifecycle_EndToEnd drives the full contact/org/membership CLI
// surface, including flag-then-positional-id commands (add-point, update)
// — flags must come before the trailing id because Go's flag package stops
// parsing at the first non-flag argument.
func TestContactLifecycle_EndToEnd(t *testing.T) {
	setTestProfileDirs(t)

	out, stderr, code := runCLI(t, "contact", "add", "--name", "Ada Lovelace", "--email", "ada@example.com")
	if code != 0 {
		t.Fatalf("contact add: code=%d stderr=%s", code, stderr)
	}
	var person map[string]any
	if err := json.Unmarshal([]byte(out), &person); err != nil {
		t.Fatalf("contact add: invalid JSON: %v (%s)", err, out)
	}
	personID, _ := person["ID"].(string)
	if personID == "" {
		t.Fatalf("contact add: missing ID in %s", out)
	}

	if _, stderr, code = runCLI(t, "contact", "add-point", "--kind", "phone", "--value", "+1 555 000 1111", personID); code != 0 {
		t.Fatalf("contact add-point: code=%d stderr=%s", code, stderr)
	}

	if _, stderr, code = runCLI(t, "contact", "update", "--name", "Ada King", personID); code != 0 {
		t.Fatalf("contact update: code=%d stderr=%s", code, stderr)
	}

	out, stderr, code = runCLI(t, "contact", "show", personID)
	if code != 0 {
		t.Fatalf("contact show: code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(out, "Ada King") {
		t.Errorf("contact show output missing updated name: %s", out)
	}
	if !strings.Contains(out, "+1 555 000 1111") {
		t.Errorf("contact show output missing added phone: %s", out)
	}

	if _, stderr, code = runCLI(t, "contact", "tag", personID, "vip"); code != 0 {
		t.Fatalf("contact tag: code=%d stderr=%s", code, stderr)
	}
	if _, stderr, code = runCLI(t, "contact", "classify", personID, "business"); code != 0 {
		t.Fatalf("contact classify: code=%d stderr=%s", code, stderr)
	}

	out, stderr, code = runCLI(t, "org", "add", "--name", "Acme Analytical Engines")
	if code != 0 {
		t.Fatalf("org add: code=%d stderr=%s", code, stderr)
	}
	var org map[string]any
	if err := json.Unmarshal([]byte(out), &org); err != nil {
		t.Fatalf("org add: invalid JSON: %v (%s)", err, out)
	}
	orgID, _ := org["ID"].(string)

	if _, stderr, code = runCLI(t, "membership", "add", "--person", personID, "--org", orgID, "--role", "employee"); code != 0 {
		t.Fatalf("membership add: code=%d stderr=%s", code, stderr)
	}

	out, stderr, code = runCLI(t, "membership", "list-person", personID)
	if code != 0 {
		t.Fatalf("membership list-person: code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(out, orgID) {
		t.Errorf("membership list-person missing org id: %s", out)
	}

	// Duplicate contact point on the same person must be rejected.
	_, stderr, code = runCLI(t, "contact", "add-point", "--kind", "email", "--value", "ADA@EXAMPLE.COM", personID)
	if code == 0 {
		t.Fatalf("expected non-zero exit for duplicate contact point, stderr=%s", stderr)
	}

	if _, stderr, code = runCLI(t, "contact", "delete", personID); code != 0 {
		t.Fatalf("contact delete: code=%d stderr=%s", code, stderr)
	}
	if _, _, code = runCLI(t, "contact", "show", personID); code == 0 {
		t.Fatal("contact show after delete: expected non-zero exit")
	}
}

// TestHumanFlag_IsReadableNotJSON exercises the --human rendering path
// added for the CLI JSON/human contract (see docs/CLI_CONTRACT.md):
// --json (the default) must stay parseable, --human must stay a non-JSON
// summary, for the same underlying data.
func TestHumanFlag_IsReadableNotJSON(t *testing.T) {
	setTestProfileDirs(t)

	out, stderr, code := runCLI(t, "contact", "add", "--name", "Grace Hopper", "--email", "grace@example.com")
	if code != 0 {
		t.Fatalf("contact add: code=%d stderr=%s", code, stderr)
	}
	var person map[string]any
	if err := json.Unmarshal([]byte(out), &person); err != nil {
		t.Fatalf("default output is not JSON: %v (%s)", err, out)
	}
	personID, _ := person["ID"].(string)

	out, stderr, code = runCLI(t, "contact", "show", "--human", personID)
	if code != 0 {
		t.Fatalf("contact show --human: code=%d stderr=%s", code, stderr)
	}
	if err := json.Unmarshal([]byte(out), &map[string]any{}); err == nil {
		t.Errorf("--human output looks like JSON, want a human-readable summary: %s", out)
	}
	if !strings.Contains(out, "Grace Hopper") || !strings.Contains(out, "grace@example.com") {
		t.Errorf("--human output missing expected fields: %s", out)
	}
}

// TestContactRef_EndToEnd exercises the reference-only lookup documented in
// docs/integrations/CONTACT_REF.md: JSON by default with the exact contract
// key set, never any contact-point or notes data, a readable --human
// variant, and a non-zero exit for unknown IDs.
func TestContactRef_EndToEnd(t *testing.T) {
	setTestProfileDirs(t)

	out, stderr, code := runCLI(t, "contact", "add", "--name", "Ada Lovelace", "--email", "ada@example.com", "--notes", "private-ref-test-notes")
	if code != 0 {
		t.Fatalf("contact add: code=%d stderr=%s", code, stderr)
	}
	var person map[string]any
	if err := json.Unmarshal([]byte(out), &person); err != nil {
		t.Fatalf("contact add: invalid JSON: %v (%s)", err, out)
	}
	personID, _ := person["ID"].(string)
	if personID == "" {
		t.Fatalf("contact add: missing ID in %s", out)
	}

	out, stderr, code = runCLI(t, "contact", "ref", personID)
	if code != 0 {
		t.Fatalf("contact ref: code=%d stderr=%s", code, stderr)
	}
	var ref map[string]any
	if err := json.Unmarshal([]byte(out), &ref); err != nil {
		t.Fatalf("contact ref: invalid JSON: %v (%s)", err, out)
	}
	wantKeys := map[string]bool{"provider": true, "schema_version": true, "id": true, "kind": true, "display_name": true}
	if len(ref) != len(wantKeys) {
		t.Errorf("contact ref keys = %v, want exactly %v", ref, wantKeys)
	}
	for k := range ref {
		if !wantKeys[k] {
			t.Errorf("contact ref: unexpected key %q", k)
		}
	}
	if ref["provider"] != "symrelate" || ref["id"] != personID || ref["kind"] != "person" || ref["display_name"] != "Ada Lovelace" {
		t.Errorf("contact ref payload = %v", ref)
	}
	if strings.Contains(out, "ada@example.com") || strings.Contains(out, "private-ref-test-notes") {
		t.Errorf("contact ref leaks private data: %s", out)
	}

	out, stderr, code = runCLI(t, "contact", "ref", "--human", personID)
	if code != 0 {
		t.Fatalf("contact ref --human: code=%d stderr=%s", code, stderr)
	}
	if err := json.Unmarshal([]byte(out), &map[string]any{}); err == nil {
		t.Errorf("--human output looks like JSON, want a human-readable summary: %s", out)
	}
	if !strings.Contains(out, "Ada Lovelace") || strings.Contains(out, "ada@example.com") {
		t.Errorf("--human output wrong: %s", out)
	}

	if _, _, code = runCLI(t, "contact", "ref", "does-not-exist"); code == 0 {
		t.Fatal("contact ref with unknown id: expected non-zero exit")
	}

	if _, _, code = runCLI(t, "contact", "delete", personID); code != 0 {
		t.Fatalf("contact delete: code=%d", code)
	}
	if _, _, code = runCLI(t, "contact", "ref", personID); code == 0 {
		t.Fatal("contact ref after delete: expected non-zero exit")
	}
}
