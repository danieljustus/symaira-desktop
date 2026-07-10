// Package recipes owns the vault-side contract for reviewed automation.
// Runners only propose changes; SymDesk validates, records and applies them.
package recipes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

const ContractVersion = 1

var validTriggers = map[string]bool{"manual": true, "save": true, "commit": true, "schedule": true}

type Recipe struct {
	Version  int      `yaml:"version" json:"version"`
	Name     string   `yaml:"name" json:"name"`
	Triggers []string `yaml:"triggers" json:"triggers"`
	Tools    []string `yaml:"tools" json:"tools"`
	WriteCap int      `yaml:"write_cap" json:"write_cap"`
}

type Change struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type Request struct {
	ContractVersion int    `json:"contract_version"`
	RunID           string `json:"run_id"`
	Vault           string `json:"vault"`
	Recipe          Recipe `json:"recipe"`
	Trigger         string `json:"trigger"`
}

type Response struct {
	ContractVersion int      `json:"contract_version"`
	Trace           []string `json:"trace"`
	Changes         []Change `json:"changes"`
}

type Manifest struct {
	Request  Request   `json:"request"`
	Response Response  `json:"response"`
	Status   string    `json:"status"`
	Created  time.Time `json:"created"`
}

func Load(path string) (Recipe, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Recipe{}, err
	}
	var r Recipe
	if err := yaml.Unmarshal(b, &r); err != nil {
		return Recipe{}, fmt.Errorf("parse recipe: %w", err)
	}
	return r, Validate(r)
}

func Validate(r Recipe) error {
	if r.Version != ContractVersion {
		return fmt.Errorf("unsupported recipe version %d", r.Version)
	}
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("recipe name is required")
	}
	if len(r.Triggers) == 0 {
		return errors.New("at least one trigger is required")
	}
	for _, trigger := range r.Triggers {
		if !validTriggers[trigger] {
			return fmt.Errorf("unsupported trigger %q", trigger)
		}
	}
	if r.WriteCap < 0 {
		return errors.New("write_cap cannot be negative")
	}
	if len(r.Tools) == 0 {
		return errors.New("at least one allowed tool is required")
	}
	seen := map[string]bool{}
	for _, tool := range r.Tools {
		if strings.TrimSpace(tool) == "" {
			return errors.New("tool allow-list cannot include an empty name")
		}
		if seen[tool] {
			return fmt.Errorf("tool %q is listed more than once", tool)
		}
		seen[tool] = true
	}
	return nil
}

// Start delegates to the optional symvibe executable. The runner receives a
// versioned JSON request and can only return proposed file changes, never a
// command for SymDesk to execute.
func Start(ctx context.Context, vaultRoot string, recipe Recipe, trigger string) (Manifest, error) {
	if err := Validate(recipe); err != nil {
		return Manifest{}, err
	}
	if !validTriggers[trigger] {
		return Manifest{}, fmt.Errorf("unsupported trigger %q", trigger)
	}
	allowed := false
	for _, t := range recipe.Triggers {
		if t == trigger {
			allowed = true
			break
		}
	}
	if !allowed {
		return Manifest{}, fmt.Errorf("recipe %q does not allow %s runs", recipe.Name, trigger)
	}
	runner, err := exec.LookPath("symvibe")
	if err != nil {
		return Manifest{}, errors.New("no compatible recipe runner found; install symvibe to run this recipe")
	}
	runID := fmt.Sprintf("%d-%s", time.Now().UTC().UnixNano(), safeName(recipe.Name))
	req := Request{ContractVersion: ContractVersion, RunID: runID, Vault: vaultRoot, Recipe: recipe, Trigger: trigger}
	dir := runDir(vaultRoot, runID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Manifest{}, err
	}
	requestPath, responsePath := filepath.Join(dir, "request.json"), filepath.Join(dir, "response.json")
	if err := writeJSON(requestPath, req); err != nil {
		return Manifest{}, err
	}
	cmd := exec.CommandContext(ctx, runner, "recipe", "run", "--request", requestPath, "--response", responsePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return Manifest{}, fmt.Errorf("recipe runner failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	b, err := os.ReadFile(responsePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("recipe runner did not produce a response: %w", err)
	}
	var response Response
	if err := json.Unmarshal(b, &response); err != nil {
		return Manifest{}, fmt.Errorf("parse runner response: %w", err)
	}
	if response.ContractVersion != ContractVersion {
		return Manifest{}, fmt.Errorf("unsupported runner contract version %d", response.ContractVersion)
	}
	if err := validateChanges(vaultRoot, recipe.WriteCap, response.Changes); err != nil {
		return Manifest{}, err
	}
	m := Manifest{Request: req, Response: response, Status: "pending", Created: time.Now().UTC()}
	if err := writeJSON(filepath.Join(dir, "manifest.json"), m); err != nil {
		return Manifest{}, err
	}
	if err := writeTrace(filepath.Join(dir, "trace.md"), m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func Accept(vaultRoot, runID string) error {
	m, path, err := loadManifest(vaultRoot, runID)
	if err != nil {
		return err
	}
	if m.Status != "pending" {
		return fmt.Errorf("run %s is %s", runID, m.Status)
	}
	if err := validateChanges(vaultRoot, m.Request.Recipe.WriteCap, m.Response.Changes); err != nil {
		return err
	}
	// Validate every destination before changing anything, then prepare each
	// replacement. If one replacement cannot be installed, restore files that
	// were already replaced so acceptance is all-or-nothing to callers.
	paths := make([]string, 0, len(m.Response.Changes))
	for _, c := range m.Response.Changes {
		p, err := vault.SecurePath(vaultRoot, c.Path)
		if err != nil {
			return err
		}
		paths = append(paths, p)
	}
	temps := make([]string, len(m.Response.Changes))
	for i, c := range m.Response.Changes {
		if err := os.MkdirAll(filepath.Dir(paths[i]), 0755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(paths[i]), ".symdesk-proposal-*")
		if err != nil {
			return err
		}
		temps[i] = tmp.Name()
		if _, err := tmp.WriteString(c.Content); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Chmod(0644); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
	}
	defer func() {
		for _, tmp := range temps {
			if tmp != "" {
				_ = os.Remove(tmp)
			}
		}
	}()
	backups := make([][]byte, len(paths))
	existed := make([]bool, len(paths))
	for i, target := range paths {
		if b, err := os.ReadFile(target); err == nil {
			backups[i], existed[i] = b, true
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for i, target := range paths {
		if err := os.Rename(temps[i], target); err != nil {
			for j := 0; j < i; j++ {
				if existed[j] {
					_ = os.WriteFile(paths[j], backups[j], 0644)
				} else {
					_ = os.Remove(paths[j])
				}
			}
			return fmt.Errorf("apply proposal atomically: %w", err)
		}
		temps[i] = ""
	}
	m.Status = "accepted"
	return writeJSON(path, m)
}

func Reject(vaultRoot, runID string) error {
	m, path, err := loadManifest(vaultRoot, runID)
	if err != nil {
		return err
	}
	if m.Status != "pending" {
		return fmt.Errorf("run %s is %s", runID, m.Status)
	}
	m.Status = "rejected"
	return writeJSON(path, m)
}

func PendingDiff(vaultRoot, runID string) ([]Change, error) {
	m, _, err := loadManifest(vaultRoot, runID)
	return m.Response.Changes, err
}

func validateChanges(root string, cap int, changes []Change) error {
	if len(changes) > cap {
		return fmt.Errorf("runner proposed %d writes, exceeding write_cap %d", len(changes), cap)
	}
	seen := map[string]bool{}
	for _, c := range changes {
		if strings.TrimSpace(c.Path) == "" {
			return errors.New("proposed change path is required")
		}
		if _, err := vault.SecurePath(root, c.Path); err != nil {
			return fmt.Errorf("invalid proposed path %q: %w", c.Path, err)
		}
		if seen[c.Path] {
			return fmt.Errorf("runner proposed duplicate change for %q", c.Path)
		}
		seen[c.Path] = true
	}
	return nil
}

func runDir(root, id string) string { return filepath.Join(root, ".symdesk", "runs", id) }
func safeName(v string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, v)
}
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}
func loadManifest(root, id string) (Manifest, string, error) {
	path := filepath.Join(runDir(root, id), "manifest.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, path, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, path, err
	}
	return m, path, nil
}
func writeTrace(path string, m Manifest) error {
	lines := append([]string{"# Recipe run", "", "Status: " + m.Status, "", "## Trace", ""}, m.Response.Trace...)
	lines = append(lines, "", "## Proposed changes", "")
	paths := make([]string, 0, len(m.Response.Changes))
	for _, c := range m.Response.Changes {
		paths = append(paths, c.Path)
	}
	sort.Strings(paths)
	for _, p := range paths {
		lines = append(lines, "- `"+p+"`")
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}
