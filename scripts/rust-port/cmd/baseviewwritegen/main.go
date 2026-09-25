// Command baseviewwritegen captures Go base and view file-write behavior.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
)

type fixture struct {
	SchemaVersion  int             `json:"schema_version"`
	Oracle         string          `json:"oracle"`
	Steps          []step          `json:"steps"`
	MigrationCases []migrationCase `json:"migration_cases"`
	SnapshotCases  []snapshotCase  `json:"snapshot_cases"`
}

type step struct {
	Operation string        `json:"operation"`
	Base      *dbviews.Base `json:"base,omitempty"`
	View      *dbviews.View `json:"view,omitempty"`
	Reference string        `json:"reference,omitempty"`
	Output    *dbviews.Base `json:"output,omitempty"`
	Markdown  string        `json:"markdown,omitempty"`
	Exists    bool          `json:"exists"`
	UnixMode  uint32        `json:"unix_mode"`
}

type migrationCase struct {
	ID              string         `json:"id"`
	LegacyJSON      string         `json:"legacy_json"`
	ExistingBases   []dbviews.Base `json:"existing_bases"`
	ExpectedBases   []dbviews.Base `json:"expected_bases"`
	ExpectedCreated []bool         `json:"expected_created"`
	LegacyExists    bool           `json:"legacy_exists"`
	LegacyPreserved bool           `json:"legacy_preserved"`
	LegacyMode      uint32         `json:"legacy_mode"`
	SymdeskMode     uint32         `json:"symdesk_mode"`
	BasesDirExists  bool           `json:"bases_dir_exists"`
	BasesDirMode    uint32         `json:"bases_dir_mode"`
	BaseModes       []uint32       `json:"base_modes"`
}

type snapshotEvent struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Markdown string `json:"markdown,omitempty"`
}

type snapshotCase struct {
	ID           string          `json:"id"`
	Operation    string          `json:"operation"`
	ExistingBase *dbviews.Base   `json:"existing_base,omitempty"`
	Base         *dbviews.Base   `json:"base,omitempty"`
	View         *dbviews.View   `json:"view,omitempty"`
	Reference    string          `json:"reference,omitempty"`
	Events       []string        `json:"events"`
	Snapshots    []snapshotEvent `json:"snapshots"`
	Exists       bool            `json:"exists"`
	Markdown     string          `json:"markdown,omitempty"`
	Error        bool            `json:"error"`
}

func main() {
	output := flag.String("output", "testdata/port/vault/base-view-write.json", "fixture path")
	check := flag.Bool("check", false, "compare generated fixture without writing")
	flag.Parse()
	root, err := repoRoot()
	if err != nil {
		fatal("find repository root: %v", err)
	}
	generated, err := build()
	if err != nil {
		fatal("build fixture: %v", err)
	}
	data, err := json.MarshalIndent(generated, "", "  ")
	if err != nil {
		fatal("encode fixture: %v", err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, filepath.FromSlash(*output))
	if *check {
		current, err := os.ReadFile(path) //nolint:gosec // explicit fixture output path selected by this command.
		if err != nil {
			fatal("read fixture: %v", err)
		}
		if !bytes.Equal(current, data) {
			fatal("base/view write fixture drift; regenerate from Go production APIs")
		}
		fmt.Printf("PASS base/view write fixture (%d steps)\n", len(generated.Steps))
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Printf("PASS base/view write fixture generated (%d steps)\n", len(generated.Steps))
}

func build() (result fixture, err error) {
	root, err := os.MkdirTemp("", "base-view-write-oracle-")
	if err != nil {
		return fixture{}, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(root)) }()
	manager := dbviews.NewManager(root)
	base := &dbviews.Base{
		ID: "invoices", Path: "bases/invoices.md", Title: "Invoices",
		Created: "2026-01-02T03:04:05Z", Tags: []string{"base"},
		Properties: map[string]dbviews.PropertyConfig{"status": {Type: "select", Options: []string{"open", "paid"}}},
		Views:      []dbviews.View{{ID: "all", Name: "All invoices", Type: "table", Source: "tag:invoice", Filters: []dbviews.Filter{}, Sorts: []dbviews.Sort{}, Columns: []string{"title"}}},
	}
	steps := []step{}
	if err := manager.SaveBase(base); err != nil {
		return fixture{}, err
	}
	first, err := capture(root, "save_base", base, nil, "")
	if err != nil {
		return fixture{}, err
	}
	steps = append(steps, first)

	updated := dbviews.View{ID: "all", Name: "Open invoices", Type: "table", Source: "tag:invoice", Filters: []dbviews.Filter{{Key: "status", Operator: "equals", Value: "open"}}, Sorts: []dbviews.Sort{}, Columns: []string{"title"}}
	if err := manager.Save(updated); err != nil {
		return fixture{}, err
	}
	step, err := capture(root, "save_view", nil, &updated, "")
	if err != nil {
		return fixture{}, err
	}
	steps = append(steps, step)

	added := dbviews.View{ID: "paid", Name: "Paid invoices", Type: "table", Source: "tag:invoice", Filters: []dbviews.Filter{}, Sorts: []dbviews.Sort{}, Columns: []string{"title"}}
	if err := manager.Save(added); err != nil {
		return fixture{}, err
	}
	step, err = capture(root, "save_view", nil, &added, "")
	if err != nil {
		return fixture{}, err
	}
	steps = append(steps, step)

	if err := manager.Delete("all"); err != nil {
		return fixture{}, err
	}
	step, err = capture(root, "delete_view", nil, nil, "all")
	if err != nil {
		return fixture{}, err
	}
	steps = append(steps, step)

	if err := manager.DeleteBase("invoices"); err != nil {
		return fixture{}, err
	}
	step, err = capture(root, "delete_base", nil, nil, "invoices")
	if err != nil {
		return fixture{}, err
	}
	steps = append(steps, step)
	migrationCases, err := buildMigrationCases()
	if err != nil {
		return fixture{}, err
	}
	snapshotCases, err := buildSnapshotCases()
	if err != nil {
		return fixture{}, err
	}
	return fixture{SchemaVersion: 1, Oracle: "internal/dbviews.Manager file APIs", Steps: steps, MigrationCases: migrationCases, SnapshotCases: snapshotCases}, nil
}

func buildSnapshotCases() ([]snapshotCase, error) {
	cases := []snapshotCase{}
	for _, operation := range []string{"create_base", "save_base", "save_view", "delete_view", "delete_base", "missing_delete_view"} {
		root, err := os.MkdirTemp("", "base-view-snapshot-oracle-")
		if err != nil {
			return nil, err
		}
		manager := dbviews.NewManager(root)
		initial := dbviews.Base{
			ID: "invoices", Path: "bases/invoices.md", Title: "Invoices",
			Created: "2026-01-02T03:04:05Z", Tags: []string{"base"},
			Views: []dbviews.View{{ID: "all", Name: "All invoices", Type: "table", Source: "tag:invoice", Filters: []dbviews.Filter{}, Sorts: []dbviews.Sort{}, Columns: []string{"title"}}},
		}
		if operation != "missing_delete_view" && operation != "create_base" {
			if err := manager.SaveBase(&initial); err != nil {
				_ = os.RemoveAll(root)
				return nil, err
			}
		}
		var base *dbviews.Base
		var view *dbviews.View
		reference := "all"
		if operation == "create_base" {
			created := initial
			base = &created
		} else if operation == "save_base" {
			updated := initial
			updated.Title = "Invoices Updated"
			base = &updated
		} else if operation == "save_view" {
			updated := initial.Views[0]
			updated.Name = "Open invoices"
			view = &updated
		} else if operation == "delete_base" {
			reference = "invoices"
		}
		caseResult := snapshotCase{ID: operation, Operation: operation, ExistingBase: func() *dbviews.Base {
			if operation == "missing_delete_view" || operation == "create_base" {
				return nil
			}
			copy := initial
			return &copy
		}(), Base: base, View: view, Reference: reference, Events: []string{}, Snapshots: []snapshotEvent{}}
		manager.SetSnapshotFn(func(absPath string) {
			rel, err := filepath.Rel(root, absPath)
			if err != nil {
				return
			}
			event := snapshotEvent{Path: filepath.ToSlash(rel)}
			data, err := os.ReadFile(absPath) //nolint:gosec // callback path came from the manager under a fresh temporary root.
			if err == nil {
				event.Exists = true
				event.Markdown = string(data)
			}
			caseResult.Snapshots = append(caseResult.Snapshots, event)
			caseResult.Events = append(caseResult.Events, "snapshot")
		})
		switch operation {
		case "create_base", "save_base":
			err = manager.SaveBase(base)
		case "save_view":
			err = manager.Save(*view)
		case "delete_view":
			err = manager.Delete(reference)
		case "delete_base":
			err = manager.DeleteBase(reference)
		case "missing_delete_view":
			err = manager.Delete("missing")
		}
		caseResult.Events = append(caseResult.Events, "operation")
		caseResult.Error = err != nil
		path := filepath.Join(root, "bases", "invoices.md")
		data, readErr := os.ReadFile(path) //nolint:gosec // fixed path under a fresh temporary oracle root.
		caseResult.Exists = readErr == nil
		if readErr != nil && !os.IsNotExist(readErr) {
			_ = os.RemoveAll(root)
			return nil, readErr
		}
		if caseResult.Exists {
			caseResult.Markdown = string(data)
		}
		if err := os.RemoveAll(root); err != nil {
			return nil, err
		}
		cases = append(cases, caseResult)
	}
	return cases, nil
}

func buildMigrationCases() ([]migrationCase, error) {
	grouping := `[
  {"id":"already-present","name":"Already present","source":"invoices/"},
  {"id":"legacy-invoice-open","name":"Open invoices","source":"invoices/","filters":[{"key":"status","operator":"equals","value":"open"}]},
  {"id":"legacy-invoice-paid","name":"Paid invoices","source":"invoices/"},
  {"id":"legacy-tag","name":"Tagged invoices","source":"tag:invoice"},
  {"id":"legacy-notebook","name":"Research","source":"notebook:research"},
  {"id":"legacy-all","name":"All Notes"}
]`
	cases := []migrationCase{
		{ID: "grouping-and-existing-view-id", LegacyJSON: grouping, ExistingBases: []dbviews.Base{{
			ID: "existing", Path: "bases/existing.md", Title: "Existing", Created: "2026-01-02T03:04:05Z", Tags: []string{"base"},
			Views: []dbviews.View{{ID: "already-present", Name: "Old invoice view", Source: "invoices/", Filters: []dbviews.Filter{}, Sorts: []dbviews.Sort{}, Columns: []string{}}},
		}}},
		{ID: "malformed-json", LegacyJSON: `[{not-json`},
		{ID: "empty-json-array", LegacyJSON: `[]`},
		{ID: "empty-file", LegacyJSON: ""},
	}
	for i := range cases {
		captured, err := captureMigrationCase(cases[i])
		if err != nil {
			return nil, err
		}
		cases[i] = captured
	}
	return cases, nil
}

func captureMigrationCase(c migrationCase) (result migrationCase, err error) {
	root, err := os.MkdirTemp("", "base-view-migration-oracle-")
	if err != nil {
		return migrationCase{}, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(root)) }()
	manager := dbviews.NewManager(root)
	for i := range c.ExistingBases {
		if err := manager.SaveBase(&c.ExistingBases[i]); err != nil {
			return migrationCase{}, err
		}
	}
	legacyDir := filepath.Join(root, ".symdesk")
	if err := os.MkdirAll(legacyDir, 0700); err != nil {
		return migrationCase{}, err
	}
	legacyPath := filepath.Join(legacyDir, "views.json")
	legacyBytes := []byte(c.LegacyJSON)
	if err := os.WriteFile(legacyPath, legacyBytes, 0600); err != nil {
		return migrationCase{}, err
	}
	if err := os.Chmod(legacyPath, 0600); err != nil {
		return migrationCase{}, err
	}
	manager = dbviews.NewManager(root)
	intact, err := os.ReadFile(legacyPath) //nolint:gosec // fixed legacy file under a fresh temporary oracle root.
	if err != nil {
		return migrationCase{}, err
	}
	c.LegacyExists = true
	c.LegacyPreserved = bytes.Equal(intact, legacyBytes)
	legacyInfo, err := os.Stat(legacyPath)
	if err != nil {
		return migrationCase{}, err
	}
	if runtime.GOOS != "windows" && legacyInfo.Mode().Perm() != 0600 {
		return migrationCase{}, fmt.Errorf("legacy mode = %#o, want 0600", legacyInfo.Mode().Perm())
	}
	c.LegacyMode = 0600
	symdeskInfo, err := os.Stat(legacyDir)
	if err != nil {
		return migrationCase{}, err
	}
	if runtime.GOOS != "windows" && symdeskInfo.Mode().Perm() != 0700 {
		return migrationCase{}, fmt.Errorf("legacy directory mode = %#o, want 0700", symdeskInfo.Mode().Perm())
	}
	c.SymdeskMode = 0700
	bases, err := manager.ListBases()
	if err != nil {
		return migrationCase{}, err
	}
	for i := range bases {
		c.ExpectedCreated = append(c.ExpectedCreated, bases[i].Created != "")
		bases[i].Created = ""
		bases[i].Path = filepath.ToSlash(bases[i].Path)
		c.ExpectedBases = append(c.ExpectedBases, *bases[i])
	}
	basesDir := filepath.Join(root, dbviews.Dir)
	if info, err := os.Stat(basesDir); err == nil {
		c.BasesDirExists = true
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0750 {
			return migrationCase{}, fmt.Errorf("bases directory mode = %#o, want 0750", info.Mode().Perm())
		}
		c.BasesDirMode = 0750
	} else if !os.IsNotExist(err) {
		return migrationCase{}, err
	}
	if c.BasesDirExists {
		entries, err := os.ReadDir(basesDir)
		if err != nil {
			return migrationCase{}, err
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return migrationCase{}, err
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
				return migrationCase{}, fmt.Errorf("base file mode = %#o, want 0600", info.Mode().Perm())
			}
			c.BaseModes = append(c.BaseModes, 0600)
		}
	}
	if c.ExistingBases == nil {
		c.ExistingBases = []dbviews.Base{}
	}
	if c.ExpectedBases == nil {
		c.ExpectedBases = []dbviews.Base{}
	}
	if c.ExpectedCreated == nil {
		c.ExpectedCreated = []bool{}
	}
	if c.BaseModes == nil {
		c.BaseModes = []uint32{}
	}
	return c, nil
}

func capture(root, operation string, base *dbviews.Base, view *dbviews.View, reference string) (step, error) {
	path := "bases/invoices.md"
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path))) //nolint:gosec // fixed path under a fresh temporary oracle root.
	if operation == "delete_base" {
		if !os.IsNotExist(err) {
			return step{}, fmt.Errorf("deleted base still exists or read failed: %v", err)
		}
		return step{Operation: operation, Reference: reference, Exists: false}, nil
	}
	if err != nil {
		return step{}, err
	}
	output, err := dbviews.ParseBase(path, data)
	if err != nil {
		return step{}, err
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return step{}, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		return step{}, fmt.Errorf("base file mode = %#o, want 0600", info.Mode().Perm())
	}
	return step{
		Operation: operation, Base: base, View: view, Reference: reference,
		Output: output, Markdown: string(data), Exists: true,
		UnixMode: 0600,
	}, nil
}

func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("locate generator source")
	}
	return filepath.Abs(filepath.Join(filepath.Dir(file), "../../../../"))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
