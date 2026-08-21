package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-relate/internal/app"
	"github.com/danieljustus/symaira-relate/internal/xdg"
)

func init() {
	Register(&Command{
		Name:  "test-redact-error",
		Short: "Mock command returning sensitive info in error for testing redaction",
		Run: func(ctx context.Context, io IO, args []string) error {
			return fmt.Errorf("sensitive: ada@example.com and +1 555-123-4567")
		},
	})
}

func TestBackupRestoreAndErase_CLI(t *testing.T) {
	ctx := context.Background()
	setTestProfileDirs(t)

	// 1. Create a person
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

	// 2. Add phone contact point
	_, stderr, code = runCLI(t, "contact", "add-point", "--kind", "phone", "--value", "+1 555 000 1111", personID)
	if code != 0 {
		t.Fatalf("contact add-point: code=%d stderr=%s", code, stderr)
	}

	// 3. Create backup
	backupFile := filepath.Join(t.TempDir(), "backup.enc")
	_, stderr, code = runCLI(t, "backup", "create", "--out", backupFile, "--passphrase", "correct-horse-battery-staple")
	if code != 0 {
		t.Fatalf("backup create: code=%d stderr=%s", code, stderr)
	}

	// Verify backup file exists
	if _, err := os.Stat(backupFile); os.IsNotExist(err) {
		t.Fatalf("backup file not created at %s", backupFile)
	}

	// 4. Delete the contact in the current DB
	_, stderr, code = runCLI(t, "contact", "delete", personID)
	if code != 0 {
		t.Fatalf("contact delete: code=%d stderr=%s", code, stderr)
	}

	// Verify contact is gone
	_, _, code = runCLI(t, "contact", "show", personID)
	if code == 0 {
		t.Fatal("contact show after delete: expected non-zero exit")
	}

	// 5. Restore with WRONG passphrase should fail and write nothing
	restoreWrongFile := filepath.Join(t.TempDir(), "restored_wrong.db")
	_, stderr, code = runCLI(t, "backup", "restore", "--in", backupFile, "--target", restoreWrongFile, "--passphrase", "wrong-passphrase")
	if code == 0 {
		t.Fatal("backup restore with wrong passphrase succeeded, expected error")
	}
	if _, err := os.Stat(restoreWrongFile); !os.IsNotExist(err) {
		t.Fatalf("backup restore with wrong passphrase created file at %s", restoreWrongFile)
	}

	// 6. Restore with CORRECT passphrase should succeed
	restoreFile := filepath.Join(t.TempDir(), "restored.db")
	_, stderr, code = runCLI(t, "backup", "restore", "--in", backupFile, "--target", restoreFile, "--passphrase", "correct-horse-battery-staple")
	if code != 0 {
		t.Fatalf("backup restore: code=%d stderr=%s", code, stderr)
	}

	// Verify restored file exists
	if _, err := os.Stat(restoreFile); os.IsNotExist(err) {
		t.Fatalf("restored file not created at %s", restoreFile)
	}

	// 7. Verify the restored DB contains the original contact
	restoredApp, err := app.OpenAt(ctx, restoreFile, xdg.Paths{})
	if err != nil {
		t.Fatalf("failed to open restored app: %v", err)
	}
	defer restoredApp.Close()

	restoredPerson, err := restoredApp.Contacts.GetPerson(ctx, personID)
	if err != nil {
		t.Fatalf("failed to find person %s in restored database: %v", personID, err)
	}
	if restoredPerson.DisplayName != "Ada Lovelace" {
		t.Errorf("restored person name = %q, want Ada Lovelace", restoredPerson.DisplayName)
	}
	if len(restoredPerson.ContactPoints) != 2 {
		t.Errorf("restored person contact points len = %d, want 2", len(restoredPerson.ContactPoints))
	}

	// 8. Test audited EraseContact
	// Create another person to test erase
	out, stderr, code = runCLI(t, "contact", "add", "--name", "Grace Hopper", "--email", "grace@example.com")
	if code != 0 {
		t.Fatalf("contact add: code=%d stderr=%s", code, stderr)
	}
	if err := json.Unmarshal([]byte(out), &person); err != nil {
		t.Fatalf("contact add: invalid JSON: %v (%s)", err, out)
	}
	erasePersonID, _ := person["ID"].(string)

	// Add alias, tag, classification
	_, stderr, code = runCLI(t, "contact", "tag", erasePersonID, "amazing")
	if code != 0 {
		t.Fatalf("contact tag: code=%d stderr=%s", code, stderr)
	}

	// Run contact erase
	_, stderr, code = runCLI(t, "contact", "erase", erasePersonID)
	if code != 0 {
		t.Fatalf("contact erase: code=%d stderr=%s", code, stderr)
	}

	// Verify they are completely erased from CLI show
	_, _, code = runCLI(t, "contact", "show", erasePersonID)
	if code == 0 {
		t.Fatal("contact show after erase: expected non-zero exit")
	}

	// Verify that audit log lists the erase event via CLI command
	out, stderr, code = runCLI(t, "audit", "list")
	if code != 0 {
		t.Fatalf("audit list: code=%d stderr=%s", code, stderr)
	}

	if !strings.Contains(out, "erase_contact") {
		t.Errorf("audit list output missing erase_contact: %s", out)
	}
	if !strings.Contains(out, erasePersonID) {
		t.Errorf("audit list output missing erased person ID: %s", out)
	}
	if strings.Contains(out, "Grace") || strings.Contains(out, "grace@example.com") {
		t.Errorf("audit list leaked contact details: %s", out)
	}

	// 9. Test error redaction
	_, stderr, code = runCLI(t, "test-redact-error")
	if code == 0 {
		t.Fatal("expected test-redact-error to fail")
	}

	// Verify the email and phone are redacted in the error message
	if strings.Contains(stderr, "ada@example.com") {
		t.Errorf("contact email leaked in stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "[redacted-email]") {
		t.Errorf("expected redacted email placeholder in stderr, got: %s", stderr)
	}
	if strings.Contains(stderr, "555-123-4567") {
		t.Errorf("contact phone leaked in stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "[redacted-phone]") {
		t.Errorf("expected redacted phone placeholder in stderr, got: %s", stderr)
	}
}

func TestExportFile_Permissions(t *testing.T) {
	setTestProfileDirs(t)

	// Create a person so the export has data
	_, stderr, code := runCLI(t, "contact", "add", "--name", "Test User", "--email", "test@example.com")
	if code != 0 {
		t.Fatalf("contact add: code=%d stderr=%s", code, stderr)
	}

	exportFile := filepath.Join(t.TempDir(), "export.json")
	_, stderr, code = runCLI(t, "export", "json", "--out", exportFile)
	if code != 0 {
		t.Fatalf("export: code=%d stderr=%s", code, stderr)
	}

	info, err := os.Stat(exportFile)
	if err != nil {
		t.Fatalf("stat export file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("export file permissions = %o, want 0600", info.Mode().Perm())
	}
}

func TestBackupFile_Permissions(t *testing.T) {
	setTestProfileDirs(t)

	// Create a person so the backup has data
	_, stderr, code := runCLI(t, "contact", "add", "--name", "Test User", "--email", "test@example.com")
	if code != 0 {
		t.Fatalf("contact add: code=%d stderr=%s", code, stderr)
	}

	backupFile := filepath.Join(t.TempDir(), "backup.enc")
	_, stderr, code = runCLI(t, "backup", "create", "--out", backupFile, "--passphrase", "test-passphrase")
	if code != 0 {
		t.Fatalf("backup create: code=%d stderr=%s", code, stderr)
	}

	info, err := os.Stat(backupFile)
	if err != nil {
		t.Fatalf("stat backup file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("backup file permissions = %o, want 0600", info.Mode().Perm())
	}
}

func TestSecureFile_FailsIfExists(t *testing.T) {
	setTestProfileDirs(t)

	// Create a person so the export has data
	_, stderr, code := runCLI(t, "contact", "add", "--name", "Test User", "--email", "test@example.com")
	if code != 0 {
		t.Fatalf("contact add: code=%d stderr=%s", code, stderr)
	}

	exportFile := filepath.Join(t.TempDir(), "export.json")

	// First export should succeed
	_, stderr, code = runCLI(t, "export", "json", "--out", exportFile)
	if code != 0 {
		t.Fatalf("first export: code=%d stderr=%s", code, stderr)
	}

	// Second export to same path should fail (O_EXCL)
	_, stderr, code = runCLI(t, "export", "json", "--out", exportFile)
	if code == 0 {
		t.Fatal("expected second export to same path to fail")
	}
}

func TestBackupRestore_RefusesOverwriteWithoutForce(t *testing.T) {
	setTestProfileDirs(t)

	// Create a person and a backup
	_, stderr, code := runCLI(t, "contact", "add", "--name", "Test User", "--email", "test@example.com")
	if code != 0 {
		t.Fatalf("contact add: code=%d stderr=%s", code, stderr)
	}

	backupFile := filepath.Join(t.TempDir(), "backup.enc")
	_, stderr, code = runCLI(t, "backup", "create", "--out", backupFile, "--passphrase", "test-passphrase")
	if code != 0 {
		t.Fatalf("backup create: code=%d stderr=%s", code, stderr)
	}

	// Restore once to create the target
	targetFile := filepath.Join(t.TempDir(), "restored.db")
	_, stderr, code = runCLI(t, "backup", "restore", "--in", backupFile, "--target", targetFile, "--passphrase", "test-passphrase")
	if code != 0 {
		t.Fatalf("first restore: code=%d stderr=%s", code, stderr)
	}

	// Second restore without --force should fail
	_, stderr, code = runCLI(t, "backup", "restore", "--in", backupFile, "--target", targetFile, "--passphrase", "test-passphrase")
	if code == 0 {
		t.Fatal("expected restore to existing target without --force to fail")
	}
	if !strings.Contains(stderr, "--force") {
		t.Errorf("expected error to mention --force, got: %s", stderr)
	}
}

func TestBackupRestore_ForceOverwritesExisting(t *testing.T) {
	setTestProfileDirs(t)

	// Create a person and a backup
	_, stderr, code := runCLI(t, "contact", "add", "--name", "Test User", "--email", "test@example.com")
	if code != 0 {
		t.Fatalf("contact add: code=%d stderr=%s", code, stderr)
	}

	backupFile := filepath.Join(t.TempDir(), "backup.enc")
	_, stderr, code = runCLI(t, "backup", "create", "--out", backupFile, "--passphrase", "test-passphrase")
	if code != 0 {
		t.Fatalf("backup create: code=%d stderr=%s", code, stderr)
	}

	// Restore once to create the target
	targetFile := filepath.Join(t.TempDir(), "restored.db")
	_, stderr, code = runCLI(t, "backup", "restore", "--in", backupFile, "--target", targetFile, "--passphrase", "test-passphrase")
	if code != 0 {
		t.Fatalf("first restore: code=%d stderr=%s", code, stderr)
	}

	// Restore again with --force should succeed
	_, stderr, code = runCLI(t, "backup", "restore", "--in", backupFile, "--target", targetFile, "--passphrase", "test-passphrase", "--force")
	if code != 0 {
		t.Fatalf("restore with --force: code=%d stderr=%s", code, stderr)
	}

	// Verify target still exists
	if _, err := os.Stat(targetFile); os.IsNotExist(err) {
		t.Fatalf("target file should exist after --force restore: %s", targetFile)
	}
}
