package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

const doctorCLIFixturePath = "testdata/port/room/doctor-cli.json"
const doctorCLIOracleRevision = "7d9d60bab2742e10f238ead245f967cce1adc4ea"

type doctorCLIContract struct {
	SchemaVersion  int               `json:"schema_version"`
	OracleRevision string            `json:"oracle_revision"`
	SourceHashes   map[string]string `json:"source_hashes"`
	IdentityKey    string            `json:"identity_key"`
	IdentityMember string            `json:"identity_member"`
	Cases          []doctorCLICase   `json:"cases"`
}

type doctorCLIIdentityFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Mode    string `json:"mode"`
}

type doctorCLICase struct {
	Name          string                  `json:"name"`
	Args          []string                `json:"args"`
	Room          string                  `json:"room"`
	Config        string                  `json:"config,omitempty"`
	DefaultEnv    string                  `json:"default_env,omitempty"`
	IdentityKey   bool                    `json:"identity_key,omitempty"`
	IdentityFiles []doctorCLIIdentityFile `json:"identity_files,omitempty"`
	Tools         bool                    `json:"tools,omitempty"`
	Index         string                  `json:"index,omitempty"`
	ExitCode      int                     `json:"exit_code"`
	Stdout        string                  `json:"stdout"`
	Stderr        string                  `json:"stderr"`
	ToolCalls     string                  `json:"tool_calls"`
	RoomFiles     []string                `json:"room_files"`
}

func TestPortDoctorCLIContract(t *testing.T) {
	root := noteCLIRoot(t)
	fixture, err := makeDoctorCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, doctorCLIFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", doctorCLIFixturePath, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("Go doctor CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeDoctorCLIContract(t *testing.T, root string) (doctorCLIContract, error) {
	t.Helper()
	seed := sha256.Sum256([]byte("symroom-doctor-cli-fixture-identity"))
	private := ed25519.NewKeyFromSeed(seed[:])
	public := private.Public().(ed25519.PublicKey)
	owner := &identity.Identity{
		Name: "oracle", MemberID: identity.ComputeMemberID(public),
		PublicKey: public, PrivateKey: private,
	}
	fixture := doctorCLIContract{
		SchemaVersion: 1, OracleRevision: doctorCLIOracleRevision,
		IdentityKey: hex.EncodeToString(seed[:]), IdentityMember: owner.MemberID,
		SourceHashes: map[string]string{
			"cmd/symroom/cmd_doctor.go":          noteCLIFileHash(t, root, "cmd/symroom/cmd_doctor.go"),
			"internal/room/doctor/doctor.go":     noteCLIFileHash(t, root, "internal/room/doctor/doctor.go"),
			"internal/room/config/config.go":     noteCLIFileHash(t, root, "internal/room/config/config.go"),
			"internal/room/journal/verifier.go":  noteCLIFileHash(t, root, "internal/room/journal/verifier.go"),
			"internal/room/identity/identity.go": noteCLIFileHash(t, root, "internal/room/identity/identity.go"),
		},
	}
	goBinary := buildNoteCLIOracle(t, root)
	temp := t.TempDir()
	toolHelper := buildDoctorToolHelper(t, root, temp)
	for _, vector := range []struct {
		name        string
		args        []string
		room        string
		config      string
		defaultEnv  string
		identityKey bool
		identity    string
		identityBad bool
		tools       bool
		index       string
	}{
		{name: "healthy-json-tools", args: []string{"doctor", "--json"}, room: "valid", config: "default_identity = \"oracle\"\n", identity: "oracle", tools: true, index: "current"},
		{name: "healthy-human", args: []string{"doctor"}, room: "valid", config: "default_identity = \"oracle\"\n", identity: "oracle", tools: true, index: "current"},
		{name: "missing-default-empty-room", args: []string{"doctor", "--json"}, room: "empty"},
		{name: "malformed-config-type", args: []string{"doctor", "--json"}, room: "empty", config: "default_identity = 7\n"},
		{name: "external-identity-provider", args: []string{"doctor", "--json"}, room: "valid", defaultEnv: "oracle", identityKey: true},
		{name: "bad-key-mode-stale-index", args: []string{"doctor", "--json"}, room: "valid", config: "default_identity = \"oracle\"\n", identity: "oracle", identityBad: true, index: "stale"},
		{name: "doctor-help", args: []string{"doctor", "--help"}, room: "empty"},
		{name: "doctor-unknown-flag", args: []string{"doctor", "--bogus"}, room: "empty"},
	} {
		work := filepath.Join(temp, vector.name)
		home := filepath.Join(work, "home")
		dataHome := filepath.Join(work, "data")
		toolsDir := filepath.Join(work, "tools")
		tempDir := filepath.Join(work, "tmp")
		for _, dir := range []string{home, dataHome, toolsDir, tempDir} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return doctorCLIContract{}, err
			}
		}
		if vector.config != "" {
			configPath := filepath.Join(home, ".config", "symroom", "config.toml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
				return doctorCLIContract{}, err
			}
			if err := os.WriteFile(configPath, []byte(vector.config), 0o600); err != nil {
				return doctorCLIContract{}, err
			}
		}
		var identityFiles []doctorCLIIdentityFile
		if vector.identity != "" {
			if err := doctorCLISaveIdentity(dataHome, owner); err != nil {
				return doctorCLIContract{}, err
			}
			identityPath := filepath.Join(dataHome, "symroom", "identities", "oracle.json")
			if vector.identityBad {
				if err := os.Chmod(identityPath, 0o444); err != nil {
					return doctorCLIContract{}, err
				}
			}
			file, err := doctorCLIReadIdentity(identityPath, vector.identityBad)
			if err != nil {
				return doctorCLIContract{}, err
			}
			identityFiles = append(identityFiles, file)
		}
		roomDir := filepath.Join(work, "room")
		if vector.room == "valid" {
			if err := makeDoctorRoom(roomDir, owner, vector.index); err != nil {
				return doctorCLIContract{}, err
			}
		} else if err := os.MkdirAll(roomDir, 0o700); err != nil {
			return doctorCLIContract{}, err
		}
		if vector.tools {
			if err := installDoctorTools(toolsDir, toolHelper); err != nil {
				return doctorCLIContract{}, err
			}
		}
		logPath := filepath.Join(work, "tool-calls")
		cmd := exec.Command(goBinary, vector.args...)
		cmd.Dir = work
		cmd.Env = []string{
			"HOME=" + home, "USERPROFILE=" + home,
			"XDG_DATA_HOME=" + dataHome, "XDG_CONFIG_HOME=" + filepath.Join(work, "xdg-config"),
			"TMPDIR=" + tempDir, "PATH=" + toolsDir,
			"TZ=UTC", "LC_ALL=C", "LANG=C",
			"SYMROOM_ROOM_DIR=" + roomDir, "DOCTOR_TOOL_LOG=" + logPath,
			"DOCTOR_IDENTITY_KEY=" + fixture.IdentityKey,
		}
		if vector.defaultEnv != "" {
			cmd.Env = append(cmd.Env, "SYMROOM_DEFAULT_IDENTITY="+vector.defaultEnv)
		}
		if vector.identityKey {
			cmd.Env = append(cmd.Env, "SYMROOM_IDENTITY_KEY="+fixture.IdentityKey)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, runErr := cmd.Output()
		code := 0
		if runErr != nil {
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else {
				return doctorCLIContract{}, fmt.Errorf("run Go doctor case %s: %w", vector.name, runErr)
			}
		}
		calls, _ := os.ReadFile(logPath)
		result := doctorCLICase{
			Name: vector.name, Args: vector.args, Room: vector.room,
			Config: vector.config, DefaultEnv: vector.defaultEnv, IdentityKey: vector.identityKey,
			IdentityFiles: identityFiles, Tools: vector.tools, Index: vector.index,
			ExitCode:  portableDoctorExitCode(code, vector.identity != "", vector.identityBad),
			Stdout:    normalizeDoctorCLIOutput(stdout, home, dataHome, toolsDir, vector.identity != ""),
			Stderr:    normalizeDoctorCLIOutput(stderr.Bytes(), home, dataHome, toolsDir, vector.identity != ""),
			ToolCalls: string(calls), RoomFiles: doctorCLIRoomFiles(roomDir),
		}
		fixture.Cases = append(fixture.Cases, result)
	}
	return fixture, nil
}

func doctorCLISaveIdentity(dataHome string, owner *identity.Identity) (result error) {
	old, had := os.LookupEnv("XDG_DATA_HOME")
	if err := os.Setenv("XDG_DATA_HOME", dataHome); err != nil {
		return err
	}
	defer func() {
		var restore error
		if had {
			restore = os.Setenv("XDG_DATA_HOME", old)
		} else {
			restore = os.Unsetenv("XDG_DATA_HOME")
		}
		if result == nil {
			result = restore
		}
	}()
	return identity.Save(owner)
}

func doctorCLIReadIdentity(path string, readOnly bool) (doctorCLIIdentityFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return doctorCLIIdentityFile{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return doctorCLIIdentityFile{}, err
	}
	mode := "private"
	if readOnly {
		mode = "readonly"
	}
	if got, want := info.Mode().Perm(), doctorCLIExpectedMode(mode); got != want {
		return doctorCLIIdentityFile{}, fmt.Errorf("identity file mode is %04o, want %04o", got, want)
	}
	return doctorCLIIdentityFile{Name: filepath.Base(path), Content: string(data), Mode: mode}, nil
}

func makeDoctorRoom(dir string, owner *identity.Identity, index string) error {
	if err := os.MkdirAll(filepath.Join(dir, ".symroom"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "journal"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "room.toml"), []byte("id = \"rm_doctor_fixture\"\n"), 0o644); err != nil {
		return err
	}
	ev := &event.Event{
		V: event.CurrentVersion, ID: "ev_doctor_fixture", Room: "rm_doctor_fixture", Author: owner.MemberID,
		Seq: 1, Prev: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Lamport: 1, TS: "2026-01-02T03:04:05.006Z", Kind: event.KindRoomCreated,
		Body: json.RawMessage(fmt.Sprintf(`{"name":"Doctor Room","public_key":"%s"}`, hex.EncodeToString(owner.PublicKey))),
	}
	if err := ev.Sign(owner); err != nil {
		return err
	}
	line, err := ev.MarshalJSONLine()
	if err != nil {
		return err
	}
	journalPath := filepath.Join(dir, "journal", owner.MemberID+".jsonl")
	if err := os.WriteFile(journalPath, line, 0o644); err != nil {
		return err
	}
	if index == "" {
		return nil
	}
	indexPath := filepath.Join(dir, ".symroom", "index.sqlite")
	if err := os.WriteFile(indexPath, []byte("derived index fixture"), 0o600); err != nil {
		return err
	}
	stamp := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)
	if index == "stale" {
		stamp = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return os.Chtimes(indexPath, stamp, stamp)
}

func buildDoctorToolHelper(t *testing.T, root, temp string) string {
	t.Helper()
	source := `package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if len(os.Args) < 2 { return }
	args := strings.Join(os.Args[1:], " ")
	if os.Getenv("DOCTOR_TOOL_LOG") != "" {
		f, err := os.OpenFile(os.Getenv("DOCTOR_TOOL_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err == nil { _, _ = fmt.Fprintf(f, "%s %s\n", name, args); _ = f.Close() }
	}
	switch os.Args[1] {
	case "get": fmt.Println(os.Getenv("DOCTOR_IDENTITY_KEY"))
	case "version": fmt.Printf("{\"version\":\"%s-1.2.3\"}\n", name)
	}
}
`
	sourcePath := filepath.Join(temp, "doctor-tool-helper.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "doctor-tool-helper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	output := filepath.Join(temp, name)
	cmd := exec.Command("go", "build", "-o", output, sourcePath)
	cmd.Dir = root
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build Go doctor tool helper: %v: %s", err, data)
	}
	return output
}

func installDoctorTools(dir, helper string) error {
	for _, tool := range []string{"symdesk", "symbrain", "symvault"} {
		name := tool
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(helper)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func doctorCLIRoomFiles(dir string) []string {
	files := []string{}
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			rel, relErr := filepath.Rel(dir, path)
			if relErr == nil {
				files = append(files, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	sort.Strings(files)
	return files
}

func normalizeDoctorCLIOutput(output []byte, home, dataHome, toolsDir string, hasIdentityFile bool) string {
	text := string(output)
	for _, replacement := range [][2]string{{home, "$HOME"}, {dataHome, "$DATA"}, {toolsDir, "$TOOLS"}} {
		text = strings.ReplaceAll(text, replacement[0], replacement[1])
	}
	if runtime.GOOS == "windows" {
		for _, tool := range []string{"symdesk", "symbrain", "symvault"} {
			text = strings.ReplaceAll(text, "$TOOLS/"+tool+".exe", "$TOOLS/"+tool)
			text = strings.ReplaceAll(text, "$TOOLS\\"+tool+".exe", "$TOOLS/"+tool)
		}
		if strings.HasPrefix(text, "{") {
			text = strings.ReplaceAll(text, `\\`, "/")
		} else {
			text = strings.ReplaceAll(text, `\`, "/")
		}
	}
	if hasIdentityFile {
		text = normalizeIdentityModeOutput(text)
	}
	return text
}

func normalizeIdentityModeOutput(text string) string {
	if strings.HasPrefix(text, "{") {
		lines := strings.Split(text, "\n")
		for index, line := range lines {
			if strings.TrimSpace(line) == `"name": "identity_key_mode",` && index+3 < len(lines) {
				indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
				lines[index+1] = indent + `  "status": "platform-mode",`
				lines[index+2] = indent + `  "message": "identity key mode depends on the host platform",`
				lines[index+3] = indent + `  "remediation": "platform-specific"`
			}
		}
		text = strings.Join(lines, "\n")
		text = strings.ReplaceAll(text, `"failed": true`, `"failed": "platform-dependent"`)
		text = strings.ReplaceAll(text, `"failed": false`, `"failed": "platform-dependent"`)
		return text
	}
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		if strings.Contains(line, " identity_key_mode: ") && index+1 < len(lines) {
			lines[index] = "[MODE] identity_key_mode: host-dependent key file mode"
			lines[index+1] = "  remediation: platform-specific"
		}
	}
	return strings.Join(lines, "\n")
}

func portableDoctorExitCode(actual int, hasIdentity, identityBad bool) int {
	if hasIdentity && !identityBad && runtime.GOOS == "windows" {
		// Windows exposes writable files as 0666, while doctor requires 0600.
		// Canonical fixture keeps the private-key case semantic across hosts.
		return 0
	}
	return actual
}

func doctorCLIExpectedMode(class string) os.FileMode {
	if runtime.GOOS == "windows" {
		if class == "readonly" {
			return 0o444
		}
		return 0o666
	}
	if class == "readonly" {
		return 0o444
	}
	return 0o600
}
