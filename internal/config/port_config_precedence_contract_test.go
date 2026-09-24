package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const (
	cfgPrecedenceFixturePath = "testdata/port/core/config-precedence.json"
	cfgPrecedenceSchema      = 1
	cfgPrecedenceCommit      = "e023816a9db2b3d71514049195886fe1b9766a5a"
)

var cfgPrecedenceEnvKeys = []string{
	"SYMDESK_OLLAMA_URL",
	"SYMDESK_RECIPE_RUNNER",
	"SYMDESK_AGENT_MAX_ITERATIONS",
	"SYMDESK_STORAGE_PATH_TEMPLATE",
}

type cfgPrecedenceValue struct {
	OllamaURL           string `json:"ollama_url"`
	RecipeRunner        string `json:"recipe_runner"`
	AgentMaxIterations  int    `json:"agent_max_iterations"`
	StoragePathTemplate string `json:"storage_path_template"`
}

type cfgPrecedenceCase struct {
	ID          string             `json:"id"`
	TOML        *string            `json:"toml,omitempty"`
	Missing     bool               `json:"missing,omitempty"`
	Environment map[string]string  `json:"environment,omitempty"`
	Config      cfgPrecedenceValue `json:"config"`
}

type cfgPrecedenceFixture struct {
	SchemaVersion int                 `json:"schema_version"`
	OracleCommit  string              `json:"oracle_commit"`
	SourceHashes  map[string]string   `json:"source_hashes"`
	Cases         []cfgPrecedenceCase `json:"cases"`
}

func cfgPrecedenceCases() []cfgPrecedenceCase {
	tomlConfig := "ollama_url = \"http://toml.example\"\nrecipe_runner = \"toml-runner\"\nagent_max_iterations = 7\nstorage_path_template = \"toml/{title}\"\n"
	return []cfgPrecedenceCase{
		{ID: "missing-defaults", Missing: true},
		{ID: "toml-values", TOML: &tomlConfig},
		{ID: "environment-overrides-toml", TOML: &tomlConfig, Environment: map[string]string{
			"SYMDESK_OLLAMA_URL": "http://env.example", "SYMDESK_RECIPE_RUNNER": "env-runner",
			"SYMDESK_AGENT_MAX_ITERATIONS": "12", "SYMDESK_STORAGE_PATH_TEMPLATE": "env/{title}",
		}},
		{ID: "empty-environment-preserves-toml", TOML: &tomlConfig, Environment: map[string]string{
			"SYMDESK_OLLAMA_URL": "", "SYMDESK_RECIPE_RUNNER": "",
			"SYMDESK_AGENT_MAX_ITERATIONS": "", "SYMDESK_STORAGE_PATH_TEMPLATE": "",
		}},
		{ID: "invalid-number-preserves-toml", TOML: &tomlConfig, Environment: map[string]string{"SYMDESK_AGENT_MAX_ITERATIONS": "not-a-number"}},
		{ID: "negative-number-preserves-toml", TOML: &tomlConfig, Environment: map[string]string{"SYMDESK_AGENT_MAX_ITERATIONS": "-1"}},
		{ID: "invalid-number-preserves-default", Missing: true, Environment: map[string]string{"SYMDESK_AGENT_MAX_ITERATIONS": "many"}},
		{ID: "valid-plus-number-overrides-default", Missing: true, Environment: map[string]string{"SYMDESK_AGENT_MAX_ITERATIONS": "+12"}},
		{ID: "valid-zero-overrides-toml", TOML: &tomlConfig, Environment: map[string]string{"SYMDESK_AGENT_MAX_ITERATIONS": "0"}},
		{ID: "whitespace-is-nonempty", TOML: &tomlConfig, Environment: map[string]string{
			"SYMDESK_OLLAMA_URL": " ", "SYMDESK_RECIPE_RUNNER": "\t", "SYMDESK_STORAGE_PATH_TEMPLATE": " ",
		}},
	}
}

func cfgPrecedenceRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func cfgPrecedenceHash(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // rel is a fixed repository fixture path
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cfgPrecedenceRun(t *testing.T, c cfgPrecedenceCase) cfgPrecedenceValue {
	t.Helper()
	for _, key := range cfgPrecedenceEnvKeys {
		t.Setenv(key, "")
	}
	for key, value := range c.Environment {
		t.Setenv(key, value)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if !c.Missing {
		if c.TOML == nil {
			t.Fatalf("case %q has neither missing=true nor TOML", c.ID)
		}
		if err := os.WriteFile(path, []byte(*c.TOML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("LoadFromPath: %v", err)
	}
	return cfgPrecedenceValue{
		OllamaURL: got.OllamaURL, RecipeRunner: got.RecipeRunner,
		AgentMaxIterations: got.AgentMaxIterations, StoragePathTemplate: got.StoragePathTemplate,
	}
}

// TestPortConfigPrecedenceContract generates the fixture only by executing the
// Go loader. A normal run checks each frozen result against that same oracle.
func TestPortConfigPrecedenceContract(t *testing.T) {
	root := cfgPrecedenceRepoRoot(t)
	fixturePath := filepath.Join(root, cfgPrecedenceFixturePath)
	cases := cfgPrecedenceCases()
	fixture := cfgPrecedenceFixture{
		SchemaVersion: cfgPrecedenceSchema,
		OracleCommit:  cfgPrecedenceCommit,
		SourceHashes:  map[string]string{"internal/config/config.go": cfgPrecedenceHash(t, root, "internal/config/config.go")},
		Cases:         cases,
	}
	if os.Getenv("PORT_GENERATE") == "1" {
		for i := range fixture.Cases {
			fixture.Cases[i].Config = cfgPrecedenceRun(t, fixture.Cases[i])
		}
		encoded, err := json.MarshalIndent(fixture, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(fixturePath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixturePath, append(encoded, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}

	data, err := os.ReadFile(fixturePath) //nolint:gosec // fixturePath is the fixed repository fixture path
	if err != nil {
		t.Fatalf("read Go-owned fixture %s (generate with PORT_GENERATE=1): %v", cfgPrecedenceFixturePath, err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if fixture.SchemaVersion != cfgPrecedenceSchema || fixture.OracleCommit != cfgPrecedenceCommit {
		t.Fatalf("unexpected fixture schema/oracle: %d/%s", fixture.SchemaVersion, fixture.OracleCommit)
	}
	if want := cfgPrecedenceHash(t, root, "internal/config/config.go"); fixture.SourceHashes["internal/config/config.go"] != want {
		t.Fatalf("config source drifted: fixture %s, current %s", fixture.SourceHashes["internal/config/config.go"], want)
	}
	if len(fixture.Cases) != len(cases) {
		t.Fatalf("fixture has %d cases, generator defines %d", len(fixture.Cases), len(cases))
	}
	for i, c := range fixture.Cases {
		if c.ID != cases[i].ID || c.Missing != cases[i].Missing || !reflect.DeepEqual(c.TOML, cases[i].TOML) || !reflect.DeepEqual(c.Environment, cases[i].Environment) {
			t.Fatalf("fixture input for case %d differs from Go generator input", i)
		}
		t.Run(c.ID, func(t *testing.T) {
			if got := cfgPrecedenceRun(t, c); !reflect.DeepEqual(got, c.Config) {
				t.Fatalf("Go loader result %#v, fixture %#v", got, c.Config)
			}
		})
	}
}
