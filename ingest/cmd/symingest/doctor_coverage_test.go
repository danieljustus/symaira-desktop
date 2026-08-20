package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckWritableDir_Failures(t *testing.T) {
	// 1. Test os.MkdirAll failure.
	// Create a read-only parent directory.
	parentDir := t.TempDir()
	readOnlyDir := filepath.Join(parentDir, "readonly")
	if err := os.MkdirAll(readOnlyDir, 0700); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	// Make it read-only.
	if err := os.Chmod(readOnlyDir, 0500); err != nil {
		t.Fatalf("failed to chmod: %v", err)
	}
	defer os.Chmod(readOnlyDir, 0700) // Ensure cleanup is possible

	// Try to create a child directory inside the read-only directory.
	childDir := filepath.Join(readOnlyDir, "child")
	report1 := &doctorReport{}
	checkWritableDir(report1, "test.mkdir", childDir)

	if report1.Status != doctorFail {
		t.Errorf("expected doctorFail, got %s", report1.Status)
	}
	if len(report1.Checks) != 1 || report1.Checks[0].Status != doctorFail {
		t.Errorf("expected failing check, got %+v", report1.Checks)
	}

	// 2. Test os.CreateTemp failure.
	// We pass the read-only directory itself as the directory.
	report2 := &doctorReport{}
	checkWritableDir(report2, "test.createtemp", readOnlyDir)

	if report2.Status != doctorFail {
		t.Errorf("expected doctorFail, got %s", report2.Status)
	}
	if len(report2.Checks) != 1 || report2.Checks[0].Status != doctorFail {
		t.Errorf("expected failing check, got %+v", report2.Checks)
	}
}

func TestCheckWritableDB_Failures(t *testing.T) {
	// 1. Test database directory creation failure.
	parentDir := t.TempDir()
	readOnlyDir := filepath.Join(parentDir, "readonly")
	if err := os.MkdirAll(readOnlyDir, 0700); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	// Make it read-only.
	if err := os.Chmod(readOnlyDir, 0500); err != nil {
		t.Fatalf("failed to chmod: %v", err)
	}
	defer os.Chmod(readOnlyDir, 0700) // Ensure cleanup is possible

	// Try to create db inside a child directory of the read-only directory.
	dbPath := filepath.Join(readOnlyDir, "child", "db.sqlite")
	report1 := &doctorReport{}
	checkWritableDB(report1, dbPath)

	if report1.Status != doctorFail {
		t.Errorf("expected doctorFail, got %s", report1.Status)
	}
	if len(report1.Checks) != 1 || report1.Checks[0].Status != doctorFail {
		t.Errorf("expected failing check, got %+v", report1.Checks)
	}

	// 2. Test database open failure.
	// We pass a path that exists but is a directory.
	dbDir := t.TempDir()
	report2 := &doctorReport{}
	checkWritableDB(report2, dbDir)

	if report2.Status != doctorFail {
		t.Errorf("expected doctorFail, got %s", report2.Status)
	}
	if len(report2.Checks) != 1 || report2.Checks[0].Status != doctorFail {
		t.Errorf("expected failing check, got %+v", report2.Checks)
	}
}
