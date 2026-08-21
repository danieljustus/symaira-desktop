package brainprofile

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-room/internal/journal"
	"github.com/danieljustus/symaira-room/internal/members"
)

type Profile struct {
	Name     string
	MemberID string
	Role     string
	RoomSlug string
}

func Generate(roomDir, memberID string) (string, *Profile, error) {
	j := journal.New(filepath.Join(roomDir, "journal"))
	merged, err := j.MergeAll()
	if err != nil {
		return "", nil, err
	}

	state := members.NewState()
	for _, e := range merged {
		if err := state.ApplyEvent(e); err != nil {
			return "", nil, err
		}
	}

	m, exists := state.Members[memberID]
	if !exists {
		return "", nil, fmt.Errorf("member %s not found in room", memberID)
	}

	roomName := "symaira-room"
	for _, e := range merged {
		if e.Kind == "room.created" {
			var b struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(e.Body, &b); err == nil && b.Name != "" {
				roomName = b.Name
			}
			break
		}
	}

	slug := strings.ToLower(strings.ReplaceAll(roomName, " ", "-"))
	profName := fmt.Sprintf("room-%s", slug)

	tomlContent := fmt.Sprintf(`[profile]
name = "%s"
member_id = "%s"
role = "%s"

[permissions]
approve_runs = false
resolve_checkpoints = false
read_journal = true
write_notes = true
`, profName, m.ID, m.Role)

	prof := &Profile{
		Name:     profName,
		MemberID: m.ID,
		Role:     string(m.Role),
		RoomSlug: slug,
	}

	return tomlContent, prof, nil
}

func Install(profName, content string) (string, error) {
	symbrainBin, err := exec.LookPath("symbrain")
	if err != nil {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("symbrain not installed and user home not found: %w", err)
		}
		dir := filepath.Join(home, ".config", "symbrain", "profiles")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", err
		}
		path := filepath.Join(dir, fmt.Sprintf("%s.toml", profName))
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return "", err
		}
		return fmt.Sprintf("symbrain is not installed on PATH. Profile written to %s", path), nil
	}

	cmd := exec.Command(symbrainBin, "profile", "add", profName)
	cmd.Stdin = strings.NewReader(content)
	if output, err := cmd.CombinedOutput(); err == nil {
		return string(output), nil
	}

	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".config", "symbrain", "profiles")
	_ = os.MkdirAll(dir, 0755)
	path := filepath.Join(dir, fmt.Sprintf("%s.toml", profName))
	_ = os.WriteFile(path, []byte(content), 0644)
	return fmt.Sprintf("Profile written to %s", path), nil
}
