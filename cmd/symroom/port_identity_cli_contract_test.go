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
	"sort"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

const identityCLIContractPath = "testdata/port/room/identity-cli.json"

type identityCLIContract struct {
	SchemaVersion  int                         `json:"schema_version"`
	OracleRevision string                      `json:"oracle_revision"`
	SourceHashes   map[string]string           `json:"source_hashes"`
	SeedFiles      []identityCLIFile           `json:"seed_files"`
	Cases          []identityCLIContractResult `json:"cases"`
}

type identityCLIFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type identityCLICase struct {
	name        string
	args        []string
	empty       bool
	listExtras  bool
	dynamicKeys bool
}

type identityCLIContractResult struct {
	Name        string            `json:"name"`
	Args        []string          `json:"args"`
	EmptyStore  bool              `json:"empty_store,omitempty"`
	ListExtras  bool              `json:"list_extras,omitempty"`
	DynamicKeys bool              `json:"dynamic_keys,omitempty"`
	ExitCode    int               `json:"exit_code"`
	Stdout      string            `json:"stdout"`
	Stderr      string            `json:"stderr"`
	FinalFiles  []identityCLIFile `json:"final_files"`
}

// TestPortIdentityCLIContract records output, exit status and identity files
// from the Go process. Fixture generation is explicit and normal checks only
// read and compare the Go oracle.
func TestPortIdentityCLIContract(t *testing.T) {
	root := identityCLIRoot(t)
	fixture, err := makeIdentityCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, identityCLIContractPath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		t.Logf("wrote %s", identityCLIContractPath)
		return
	}
	got, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", identityCLIContractPath, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Go identity CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeIdentityCLIContract(t *testing.T, root string) (identityCLIContract, error) {
	t.Helper()
	fixture := identityCLIContract{
		SchemaVersion:  1,
		OracleRevision: "439d04347bb2881495bff3acc52a43dc7bff6d39",
		SourceHashes: map[string]string{
			"cmd/symroom/main.go":                identityCLIFileHash(t, root, "cmd/symroom/main.go"),
			"cmd/symroom/cmd_identity.go":        identityCLIFileHash(t, root, "cmd/symroom/cmd_identity.go"),
			"internal/room/identity/identity.go": identityCLIFileHash(t, root, "internal/room/identity/identity.go"),
		},
	}
	for _, seedName := range []string{"alpha", "oracle"} {
		seed := sha256.Sum256([]byte("symroom-identity-cli-fixture-" + seedName))
		privateKey := ed25519.NewKeyFromSeed(seed[:])
		publicKey := privateKey.Public().(ed25519.PublicKey)
		stored := identity.StoredIdentity{
			Name: seedName, MemberID: identity.ComputeMemberID(publicKey),
			PublicKey: hex.EncodeToString(publicKey), PrivateKey: hex.EncodeToString(privateKey),
		}
		encoded, err := json.MarshalIndent(stored, "", "  ") //nolint:gosec // test fixture serializes deterministic synthetic private keys
		if err != nil {
			return identityCLIContract{}, err
		}
		fixture.SeedFiles = append(fixture.SeedFiles, identityCLIFile{Name: seedName + ".json", Content: string(encoded)})
	}
	goBinary := buildIdentityCLIOracle(t, root)
	temp := t.TempDir()
	cases := []identityCLICase{
		{name: "identity-usage", args: []string{"identity"}},
		{name: "identity-unknown", args: []string{"identity", "bogus"}},
		{name: "create-usage", args: []string{"identity", "create"}},
		{name: "create-success", args: []string{"identity", "create", "fresh"}, empty: true, dynamicKeys: true},
		{name: "list-sorted", args: []string{"identity", "list"}, listExtras: true},
		{name: "list-empty", args: []string{"identity", "list"}, empty: true},
		{name: "show-valid", args: []string{"identity", "show", "oracle"}},
		{name: "show-usage", args: []string{"identity", "show"}},
		{name: "show-missing", args: []string{"identity", "show", "missing"}},
		{name: "export-public", args: []string{"identity", "export", "--public", "oracle"}},
		{name: "export-public-equals", args: []string{"identity", "export", "--public=TRUE", "oracle"}},
		{name: "export-private", args: []string{"identity", "export", "oracle"}},
		{name: "export-flag-after-name", args: []string{"identity", "export", "oracle", "--public"}},
		{name: "export-usage", args: []string{"identity", "export"}},
		{name: "export-missing", args: []string{"identity", "export", "--public", "missing"}},
		{name: "export-bad-bool", args: []string{"identity", "export", "--public=maybe", "oracle"}},
		{name: "export-unknown-flag", args: []string{"identity", "export", "--bogus", "oracle"}},
		{name: "export-help", args: []string{"identity", "export", "--help"}},
	}
	for _, vector := range cases {
		dataHome := filepath.Join(temp, vector.name, "data")
		identitiesDir := filepath.Join(dataHome, "symroom", "identities")
		if err := os.MkdirAll(identitiesDir, 0o700); err != nil {
			return identityCLIContract{}, err
		}
		if !vector.empty {
			for _, file := range fixture.SeedFiles {
				if err := os.WriteFile(filepath.Join(identitiesDir, file.Name), []byte(file.Content), 0o600); err != nil {
					return identityCLIContract{}, err
				}
			}
		}
		if vector.listExtras {
			if err := os.Mkdir(filepath.Join(identitiesDir, "nested.json"), 0o700); err != nil {
				return identityCLIContract{}, err
			}
			if err := os.WriteFile(filepath.Join(identitiesDir, "ignored.json.bak"), []byte("ignored"), 0o600); err != nil {
				return identityCLIContract{}, err
			}
		}
		home := filepath.Join(temp, "env-"+vector.name, "home")
		tmp := filepath.Join(temp, "env-"+vector.name, "tmp")
		for _, path := range []string{home, tmp} {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return identityCLIContract{}, err
			}
		}
		cmd := exec.Command(goBinary, vector.args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
		cmd.Env = []string{"HOME=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tmp, "TZ=UTC", "LC_ALL=C", "LANG=C"}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.Output()
		code := 0
		if err != nil {
			if exitError, ok := err.(*exec.ExitError); ok {
				code = exitError.ExitCode()
			} else {
				return identityCLIContract{}, fmt.Errorf("run Go identity case %s: %w", vector.name, err)
			}
		}
		finalFiles, err := readIdentityCLIFileSnapshot(identitiesDir, vector.dynamicKeys)
		if err != nil {
			return identityCLIContract{}, err
		}
		output := string(stdout)
		if vector.dynamicKeys {
			// The OS CSPRNG is intentionally nondeterministic; retain the exact
			// command shape while checking all generated key relationships in Rust.
			fields := strings.Fields(output)
			if len(fields) != 4 || fields[0] != "Created" || fields[1] != "identity" || fields[2] != "fresh" {
				return identityCLIContract{}, fmt.Errorf("unexpected Go create output %q", output)
			}
			output = "Created identity fresh (<member_id>)\n"
		}
		fixture.Cases = append(fixture.Cases, identityCLIContractResult{
			Name: vector.name, Args: vector.args, EmptyStore: vector.empty, ListExtras: vector.listExtras, DynamicKeys: vector.dynamicKeys,
			ExitCode: code, Stdout: output, Stderr: stderr.String(), FinalFiles: finalFiles,
		})
	}
	return fixture, nil
}

func readIdentityCLIFileSnapshot(dir string, normalizeKeys bool) ([]identityCLIFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]identityCLIFile, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			return nil, err
		}
		if normalizeKeys {
			var stored identity.StoredIdentity
			if err := json.Unmarshal(data, &stored); err != nil {
				return nil, err
			}
			stored.MemberID = "<member_id>"
			stored.PublicKey = "<public_key>"
			stored.PrivateKey = "<private_key>"
			normalized, err := json.MarshalIndent(stored, "", "  ") //nolint:gosec // test fixture serializes deterministic synthetic private keys
			if err != nil {
				return nil, err
			}
			data = normalized
		}
		files = append(files, identityCLIFile{Name: entry.Name(), Content: string(data)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func buildIdentityCLIOracle(t *testing.T, root string) string {
	t.Helper()
	path := oracleExecutablePath(t, "symroom-go-identity-oracle")
	cmd := exec.Command("go", "build", "-o", path, "./cmd/symroom") //nolint:gosec // test-only command uses a fixed helper and controlled arguments
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build Go symroom identity oracle: %v\n%s", err, output)
	}
	return path
}

func identityCLIRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func identityCLIFileHash(t *testing.T, root, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, path)) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
