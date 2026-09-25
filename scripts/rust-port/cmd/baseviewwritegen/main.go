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
	SchemaVersion int    `json:"schema_version"`
	Oracle        string `json:"oracle"`
	Steps         []step `json:"steps"`
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
		current, err := os.ReadFile(path)
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
	return fixture{SchemaVersion: 1, Oracle: "internal/dbviews.Manager file APIs", Steps: steps}, nil
}

func capture(root, operation string, base *dbviews.Base, view *dbviews.View, reference string) (step, error) {
	path := "bases/invoices.md"
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
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
