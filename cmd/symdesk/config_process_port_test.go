package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type configProcessFile struct {
	Content string `json:"content"`
	Mode    uint32 `json:"mode"`
}
type configProcessStep struct {
	Args     []string                     `json:"args"`
	ExitCode int                          `json:"exit_code"`
	Stdout   string                       `json:"stdout"`
	Stderr   string                       `json:"stderr"`
	Files    map[string]configProcessFile `json:"files"`
	Requests []string                     `json:"requests,omitempty"`
}
type configProcessCase struct {
	Name           string              `json:"name"`
	Config         string              `json:"config,omitempty"`
	Environment    map[string]string   `json:"environment,omitempty"`
	OllamaResponse string              `json:"ollama_response,omitempty"`
	Steps          []configProcessStep `json:"steps"`
}
type configProcessFixture struct {
	SchemaVersion int                 `json:"schema_version"`
	Cases         []configProcessCase `json:"cases"`
}

func configProcessCases() []configProcessCase {
	step := func(args ...string) configProcessStep { return configProcessStep{Args: args} }
	modes := func(args ...string) []configProcessStep {
		result := []configProcessStep{step(args...)}
		for _, flags := range [][]string{{"--json"}, {"--output", "json"}, {"--output=yaml"}, {"--json", "--output=text"}, {"--output=json", "--json=false"}} {
			result = append(result, step(append(append([]string{}, flags...), args...)...))
		}
		return result
	}
	cases := []configProcessCase{
		{Name: "ollama", OllamaResponse: `{"models":[{"name":"model-one"},{"name":"model-two"}]}`, Steps: modes("ai-config", "test")},
		{Name: "anthropic", Config: "llm_provider = 'anthropic'\nllm_api_key = 'fixture-only-key'\n", Steps: modes("ai-config", "test")},
		{Name: "defaults", Steps: append(append(modes("config", "get"), modes("config", "path")...), modes("ai-config", "show")...)},
		{Name: "config-write", Steps: []configProcessStep{step("config", "init"), step("config", "init"), step("config", "set", "language", "de"), step("config", "get", "language"), step("--json", "config", "get"), step("--vault", "$ROOT/override", "config", "set", "inbox", "post"), step("config", "get", "vault"), step("config", "get", "inbox")}},
		{Name: "ai-write", Steps: []configProcessStep{step("ai-config", "show"), step("ai-config", "set", "--provider", "anthropic", "--model", "test <&> model", "--max-tokens", "1024", "--api-key", "fixture-only-key"), step("ai-config", "show"), step("--json", "ai-config", "show"), step("--output=json", "ai-config", "set", "--api-key="), step("ai-config", "show"), step("--vault=$ROOT/override", "ai-config", "set", "--provider=none"), step("config", "get", "vault"), step("ai-config", "set", "--max-tokens=-3"), step("ai-config", "show"), step("--json", "ai-config", "test")}},
		{Name: "environment", Config: "language = 'en'\nvault = 'file-vault'\nllm_api_key = 'fixture-only-file-key'\n", Environment: map[string]string{"SYMDESK_LANG": "de", "SYMDESK_VAULT": "env-vault", "SYMDESK_LLM_MODEL": "env-model"}, Steps: []configProcessStep{step("--vault=flag-vault", "config", "get", "vault"), step("--vault=flag-vault", "ai-config", "show"), step("config", "set", "language", "en"), step("config", "get", "language"), step("ai-config", "set", "--model=flag-model"), step("ai-config", "show")}},
		{Name: "errors", Steps: []configProcessStep{step("config", "get", "unknown"), step("--json", "config", "get", "llm_api_key"), step("config", "get", "a", "b"), step("config", "set"), step("--json", "config", "set", "language"), step("config", "set", "a", "b", "c"), step("config", "set", "max_tokens", "not-an-int"), step("--output=json", "config", "set", "unknown", "value"), step("config", "get", "--bad"), step("ai-config", "set", "--max-tokens", "abc"), step("--json", "ai-config", "set", "--max-tokens", "abc"), step("config", "path", "extra"), step("ai-config", "show", "extra"), step("--output=bad", "config", "get"), step("--json=false", "config", "get", "unknown")}},
		{Name: "corrupt", Config: "max_tokens = 'invalid'\n", Steps: []configProcessStep{step("config", "get"), step("--json", "config", "path"), step("ai-config", "show"), step("config", "set", "max_tokens", "12")}},
	}
	// Inherited flags before, between and after commands and positionals; output
	// overrides --json regardless of argument ordering, repeated flags use last.
	permutations := configProcessCase{Name: "permutations"}
	for _, args := range [][]string{
		{"--json", "ai-config", "show"}, {"ai-config", "--json", "show"}, {"ai-config", "show", "--json"},
		{"--output", "json", "ai-config", "show"}, {"ai-config", "--output=json", "show"}, {"ai-config", "show", "--output", "json"},
		{"--json", "ai-config", "show", "--output=text"}, {"--output=text", "ai-config", "show", "--json"},
		{"--output=text", "ai-config", "show", "--output=json"}, {"--json", "--json=false", "ai-config", "show"},
		{"config", "get", "language", "--output=json"}, {"config", "--vault=one", "get", "vault", "--vault=two"},
		{"config", "get", "--", "--json"}, {"config", "set", "max_tokens", "+23"}, {"config", "get", "max_tokens"},
		{"config", "set", "review_threshold", "-1"}, {"config", "set", "review_threshold", "--", "-1"}, {"config", "get", "review_threshold"},
		{"ai-config", "set", "--max-tokens=010"}, {"ai-config", "show"}, {"ai-config", "set", "--max-tokens=0x20"}, {"ai-config", "show"},
	} {
		permutations.Steps = append(permutations.Steps, step(args...))
	}
	allKeys := configProcessCase{Name: "all-public-keys"}
	for _, key := range []string{"vault", "inbox", "review_threshold", "llm_provider", "llm_model", "ollama_url", "language", "max_tokens", "agent_max_iterations", "history_max_per_file", "history_max_age_days", "trash_retention_days", "results_max_age_days", "results_max_per_task", "storage_path_template"} {
		value := "17"
		if key == "vault" || key == "inbox" || key == "llm_provider" || key == "llm_model" || key == "ollama_url" || key == "language" || key == "storage_path_template" {
			value = "value <&> ü"
		}
		allKeys.Steps = append(allKeys.Steps, step("config", "get", key), step("config", "set", key, value), step("--json", "config", "get", key))
	}
	return append(cases, permutations, allKeys)
}

func TestPortConfigProcessContract(t *testing.T) {
	binary := buildIndexRetrievalBinary(t, t.TempDir())
	fixture := configProcessFixture{SchemaVersion: 1}
	for _, c := range configProcessCases() {
		root := t.TempDir()
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		home := filepath.Join(root, "home")
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, "config", "symdesk", "config.toml")
		if c.Config != "" {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(c.Config), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		environment := []string{"HOME=" + home, "USERPROFILE=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_DATA_HOME=" + filepath.Join(home, "data"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "TZ=UTC", "LANG=C", "LC_ALL=C", "PATH=" + root}
		for name, value := range c.Environment {
			environment = append(environment, name+"="+value)
		}
		var requests []string
		var requestLock sync.Mutex
		if c.OllamaResponse != "" {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestLock.Lock()
				requests = append(requests, r.Method+" "+r.URL.RequestURI())
				requestLock.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(c.OllamaResponse))
			}))
			t.Cleanup(server.Close)
			environment = append(environment, "SYMDESK_OLLAMA_URL="+server.URL)
		}
		for i := range c.Steps {
			step := &c.Steps[i]
			args := append([]string{}, step.Args...)
			for j := range args {
				args[j] = strings.ReplaceAll(args[j], "$ROOT", root)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			//nolint:gosec // binary is the locally built symdesk fixture; arguments come from fixed test cases.
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Dir, cmd.Env = root, environment
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				if ctx.Err() != nil {
					t.Fatal(ctx.Err())
				}
				exit, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatal(err)
				}
				step.ExitCode = exit.ExitCode()
			}
			cancel()
			step.Stdout = normalizeRetryText(stdout.String(), root)
			step.Stderr = normalizeRetryText(stderr.String(), root)
			requestLock.Lock()
			step.Requests = append([]string(nil), requests...)
			requests = nil
			requestLock.Unlock()
			step.Files = map[string]configProcessFile{}
			err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				info, err := d.Info()
				if err != nil {
					return err
				}
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				step.Files[filepath.ToSlash(rel)] = configProcessFile{Content: normalizeRetryText(string(data), root), Mode: uint32(info.Mode().Perm())}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		fixture.Cases = append(fixture.Cases, c)
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := "../../testdata/port/cli/config-process.json"
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		current, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "windows" {
			var expected configProcessFixture
			if err := json.Unmarshal(current, &expected); err != nil {
				t.Fatal(err)
			}
			for _, fixture := range []*configProcessFixture{&expected, &fixture} {
				for i := range fixture.Cases {
					for j := range fixture.Cases[i].Steps {
						for name, file := range fixture.Cases[i].Steps[j].Files {
							file.Mode = 0
							fixture.Cases[i].Steps[j].Files[name] = file
						}
					}
				}
			}
			current, err = json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err = json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(current, encoded) {
			t.Fatalf("config process fixture drift: %s", path)
		}
	}
	count := 0
	for _, c := range fixture.Cases {
		count += len(c.Steps)
	}
	t.Logf("verified %d cases / %d process steps with filesystem snapshots", len(fixture.Cases), count)
}
