package journal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendAndReadDay(t *testing.T) {
	vaultRoot := t.TempDir()
	now := time.Date(2026, 8, 14, 10, 30, 0, 0, time.UTC)

	entry1 := Entry{
		Timestamp: now,
		Event:     "file_added",
		Path:      "notes/test1.md",
		Title:     "Test 1",
		Details:   "created note",
	}
	entry2 := Entry{
		Timestamp: now.Add(2 * time.Hour),
		Event:     "file_changed",
		Path:      "notes/test1.md",
		Title:     "Test 1",
		Details:   "updated content",
	}

	if err := Append(vaultRoot, entry1); err != nil {
		t.Fatalf("Append failed: %v", err)
	}
	if err := Append(vaultRoot, entry2); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Verify file exists on disk
	expectedFile := filepath.Join(vaultRoot, ".symdesk", "journal", "2026-08-14.ndjson")
	if _, err := os.Stat(expectedFile); err != nil {
		t.Fatalf("expected journal file at %s: %v", expectedFile, err)
	}

	entries, err := ReadDay(vaultRoot, now)
	if err != nil {
		t.Fatalf("ReadDay failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Event != "file_added" || entries[0].Path != "notes/test1.md" {
		t.Errorf("entry 0 mismatch: %+v", entries[0])
	}
	if entries[1].Event != "file_changed" || entries[1].Path != "notes/test1.md" {
		t.Errorf("entry 1 mismatch: %+v", entries[1])
	}
}

func TestAppendNormalizesAbsolutePath(t *testing.T) {
	vaultRoot := t.TempDir()
	absPath := filepath.Join(vaultRoot, "inbox", "item.md")
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)

	entry := Entry{
		Timestamp: now,
		Event:     "file_added",
		Path:      absPath,
	}

	if err := Append(vaultRoot, entry); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	entries, err := ReadDay(vaultRoot, now)
	if err != nil {
		t.Fatalf("ReadDay failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Path != "inbox/item.md" {
		t.Errorf("expected relative path 'inbox/item.md', got %q", entries[0].Path)
	}
}

func TestReadRange(t *testing.T) {
	vaultRoot := t.TempDir()
	day1 := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	_ = Append(vaultRoot, Entry{Timestamp: day1, Event: "file_added", Path: "day1.md"})
	_ = Append(vaultRoot, Entry{Timestamp: day2, Event: "file_changed", Path: "day2.md"})
	_ = Append(vaultRoot, Entry{Timestamp: day3, Event: "file_removed", Path: "day3.md"})

	// Query range covering day1 and day2
	rangeStart := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	rangeEnd := time.Date(2026, 8, 15, 23, 59, 59, 0, time.UTC)

	entries, err := ReadRange(vaultRoot, rangeStart, rangeEnd)
	if err != nil {
		t.Fatalf("ReadRange failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries in range, got %d", len(entries))
	}
	if entries[0].Path != "day1.md" || entries[1].Path != "day2.md" {
		t.Errorf("unexpected entries in range: %+v", entries)
	}
}

func TestPrune(t *testing.T) {
	vaultRoot := t.TempDir()
	now := time.Now().UTC()
	oldDate := now.Add(-100 * 24 * time.Hour)
	recentDate := now.Add(-5 * 24 * time.Hour)

	_ = Append(vaultRoot, Entry{Timestamp: oldDate, Event: "file_added", Path: "old.md"})
	_ = Append(vaultRoot, Entry{Timestamp: recentDate, Event: "file_added", Path: "recent.md"})

	// Prune older than 30 days
	removed, err := Prune(vaultRoot, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}
	if removed != 1 {
		t.Errorf("expected 1 file removed, got %d", removed)
	}

	oldEntries, err := ReadDay(vaultRoot, oldDate)
	if err != nil {
		t.Fatalf("ReadDay oldDate: %v", err)
	}
	if len(oldEntries) != 0 {
		t.Errorf("expected old journal to be pruned, got %d entries", len(oldEntries))
	}

	recentEntries, err := ReadDay(vaultRoot, recentDate)
	if err != nil {
		t.Fatalf("ReadDay recentDate: %v", err)
	}
	if len(recentEntries) != 1 {
		t.Errorf("expected recent journal to remain, got %d entries", len(recentEntries))
	}
}
