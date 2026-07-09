package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTemplatesAndDailyNotes(t *testing.T) {
	svc := newTestService(t)

	// Create a template
	templateDir := filepath.Join(svc.VaultRoot, "templates")
	if err := os.MkdirAll(templateDir, 0755); err != nil {
		t.Fatal(err)
	}

	tplContent := `---\ncustom_field: "yes"\n---\nHello {{title}}, today is {{date}} at {{time}}.`
	if err := os.WriteFile(filepath.Join(templateDir, "meeting.md"), []byte(tplContent), 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Create a note using the template
	path, err := svc.NoteNew("Board Meeting", "Some extra content", "meeting")
	if err != nil {
		t.Fatalf("NoteNew with template failed: %v", err)
	}

	contentBytes, err := os.ReadFile(filepath.Join(svc.VaultRoot, path))
	if err != nil {
		t.Fatal(err)
	}
	contentStr := string(contentBytes)

	// Check placeholders
	if !strings.Contains(contentStr, "Hello Board Meeting") {
		t.Errorf("Placeholder {{title}} not substituted, got: %s", contentStr)
	}
	if !strings.Contains(contentStr, time.Now().UTC().Format("2006-01-02")) {
		t.Errorf("Placeholder {{date}} not substituted")
	}
	// Check frontmatter injection
	if !strings.Contains(contentStr, "custom_field: \"yes\"") {
		t.Errorf("Template frontmatter missing")
	}
	if !strings.Contains(contentStr, "title: \"Board Meeting\"") {
		t.Errorf("Title not injected into frontmatter")
	}
	if !strings.Contains(contentStr, "Some extra content") {
		t.Errorf("CLI content not appended")
	}

	// 2. Create daily note
	dailyPath, err := svc.NoteDaily("2026-07-09")
	if err != nil {
		t.Fatalf("NoteDaily failed: %v", err)
	}
	if dailyPath != "2026-07-09.md" {
		t.Errorf("Expected 2026-07-09.md, got %s", dailyPath)
	}
}
