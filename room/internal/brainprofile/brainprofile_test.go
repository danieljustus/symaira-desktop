package brainprofile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/room"
)

func TestBrainProfileGenerate(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, _ := identity.Generate("owner")

	if _, err := room.Init(tempDir, "Project Alpha", ownerID); err != nil {
		t.Fatalf("Init room failed: %v", err)
	}

	content, prof, err := Generate(tempDir, ownerID.MemberID)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if prof.Name != "room-project-alpha" {
		t.Errorf("expected profile name room-project-alpha, got %s", prof.Name)
	}

	if !strings.Contains(content, `name = "room-project-alpha"`) {
		t.Errorf("content missing profile name: %s", content)
	}
	if !strings.Contains(content, `member_id = "`+ownerID.MemberID+`"`) {
		t.Errorf("content missing member_id: %s", content)
	}
}

// writeFakeBin drops an executable shell script named binName into dir.
func writeFakeBin(t *testing.T, dir, binName, script string) string {
	t.Helper()
	path := filepath.Join(dir, binName)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", binName, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod fake %s: %v", binName, err)
	}
	return path
}

func TestInstallWritesProfileWhenSymbrainAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // symbrain absent

	content := "[profile]\nname = \"room-demo\"\n"
	msg, err := Install("room-demo", content)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	if !strings.Contains(msg, "symbrain is not installed") {
		t.Errorf("expected fallback message, got %q", msg)
	}

	path := filepath.Join(home, ".config", "symbrain", "profiles", "room-demo.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written profile: %v", err)
	}
	if string(data) != content {
		t.Errorf("profile content mismatch:\nwant: %q\ngot:  %q", content, string(data))
	}
}

func TestInstallUsesSymbrainWhenPresent(t *testing.T) {
	tempDir := t.TempDir()
	writeFakeBin(t, tempDir, "symbrain", "#!/bin/sh\ncat >/dev/null\necho \"added profile $3\"\n")
	t.Setenv("PATH", tempDir)
	t.Setenv("HOME", t.TempDir())

	msg, err := Install("room-present", "[profile]\nname = \"room-present\"\n")
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	if !strings.Contains(msg, "added profile room-present") {
		t.Errorf("expected symbrain output in message, got %q", msg)
	}
}

func TestInstallFallsBackToFileWhenSymbrainFails(t *testing.T) {
	home := t.TempDir()
	tempDir := t.TempDir()
	writeFakeBin(t, tempDir, "symbrain", "#!/bin/sh\ncat >/dev/null\nexit 1\n")
	t.Setenv("PATH", tempDir)
	t.Setenv("HOME", home)

	content := "[profile]\nname = \"room-fallback\"\n"
	msg, err := Install("room-fallback", content)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	if !strings.Contains(msg, "Profile written to") {
		t.Errorf("expected fallback message, got %q", msg)
	}

	path := filepath.Join(home, ".config", "symbrain", "profiles", "room-fallback.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written profile: %v", err)
	}
	if string(data) != content {
		t.Errorf("profile content mismatch:\nwant: %q\ngot:  %q", content, string(data))
	}
}
