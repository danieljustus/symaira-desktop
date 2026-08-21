package assets

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVendoredCmarkerEmbedded is the regression guard for the offline cmarker
// vendoring: the embedded package tree must never silently go empty again.
func TestVendoredCmarkerEmbedded(t *testing.T) {
	fsys := PackagesFS()

	toml, err := fsys.ReadFile("packages/preview/cmarker/0.1.9/typst.toml")
	if err != nil {
		t.Fatalf("embedded cmarker typst.toml missing: %v", err)
	}
	if len(toml) == 0 {
		t.Fatal("embedded cmarker typst.toml is empty")
	}
	if !strings.Contains(string(toml), `version = "0.1.9"`) {
		t.Errorf("typst.toml does not pin cmarker 0.1.9, got:\n%s", toml)
	}

	lib, err := fsys.ReadFile("packages/preview/cmarker/0.1.9/lib.typ")
	if err != nil {
		t.Fatalf("embedded cmarker lib.typ missing: %v", err)
	}
	if len(lib) == 0 {
		t.Fatal("embedded cmarker lib.typ is empty")
	}

	wasm, err := fsys.ReadFile("packages/preview/cmarker/0.1.9/plugin.wasm")
	if err != nil {
		t.Fatalf("embedded cmarker plugin.wasm missing: %v", err)
	}
	if len(wasm) == 0 {
		t.Fatal("embedded cmarker plugin.wasm is empty")
	}
}

// TestVendoredMitexEmbedded guards the complete offline mitex package,
// including the WASM plugin and its Typst support files.
func TestVendoredMitexEmbedded(t *testing.T) {
	fsys := PackagesFS()
	base := "packages/preview/mitex/0.2.7"

	toml, err := fsys.ReadFile(base + "/typst.toml")
	if err != nil {
		t.Fatalf("embedded mitex typst.toml missing: %v", err)
	}
	if !strings.Contains(string(toml), `version = "0.2.7"`) {
		t.Errorf("typst.toml does not pin mitex 0.2.7, got:\n%s", toml)
	}
	if !strings.Contains(string(toml), `license = "Apache-2.0"`) {
		t.Errorf("typst.toml does not declare Apache-2.0, got:\n%s", toml)
	}

	for _, name := range []string{"LICENSE", "lib.typ", "mitex.typ", "mitex.wasm", "specs/mod.typ", "specs/prelude.typ", "specs/latex/standard.typ"} {
		data, err := fsys.ReadFile(base + "/" + name)
		if err != nil {
			t.Fatalf("embedded mitex %s missing: %v", name, err)
		}
		if len(data) == 0 {
			t.Fatalf("embedded mitex %s is empty", name)
		}
	}
}

// TestMaterializePackages verifies the on-disk layout mirrors the Typst
// package layout (packages/{namespace}/{name}/{version}/) so the tree can be
// passed to typst via --package-path.
func TestMaterializePackages(t *testing.T) {
	dir := t.TempDir()
	if err := MaterializePackages(dir); err != nil {
		t.Fatalf("MaterializePackages failed: %v", err)
	}

	for _, pkg := range []struct {
		name  string
		files []string
	}{
		{
			name:  "cmarker",
			files: []string{"typst.toml", "lib.typ", "plugin.wasm"},
		},
		{
			name:  "mitex",
			files: []string{"typst.toml", "lib.typ", "mitex.typ", "mitex.wasm", "LICENSE", "specs/mod.typ", "specs/latex/standard.typ"},
		},
	} {
		base := filepath.Join(dir, "packages", "preview", pkg.name, map[string]string{"cmarker": "0.1.9", "mitex": "0.2.7"}[pkg.name])
		for _, name := range pkg.files {
			fi, err := os.Stat(filepath.Join(base, name))
			if err != nil {
				t.Errorf("materialized %s file %s missing: %v", pkg.name, name, err)
				continue
			}
			if fi.Size() == 0 {
				t.Errorf("materialized %s file %s is empty", pkg.name, name)
			}
		}
	}
}

// TestMaterialize writes every embedded Typst template into a target dir and
// asserts byte equality with the embedded content. The template list is
// derived from the embed itself so newly added templates are covered
// automatically.
func TestMaterialize(t *testing.T) {
	names, err := fs.Glob(fsys, "templates/*.typ")
	if err != nil {
		t.Fatalf("listing embedded templates: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no embedded templates found")
	}

	// Nested, non-existent target exercises the MkdirAll path.
	dir := filepath.Join(t.TempDir(), "templates")
	if err := Materialize(dir); err != nil {
		t.Fatalf("Materialize(%q) failed: %v", dir, err)
	}

	for _, name := range names {
		t.Run(filepath.Base(name), func(t *testing.T) {
			want, err := fsys.ReadFile(name)
			if err != nil {
				t.Fatalf("reading embedded %s: %v", name, err)
			}
			got, err := os.ReadFile(filepath.Join(dir, filepath.Base(name)))
			if err != nil {
				t.Fatalf("materialized template %s missing: %v", filepath.Base(name), err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("materialized %s differs from embedded content: got %d bytes, want %d bytes",
					filepath.Base(name), len(got), len(want))
			}
		})
	}
}

// TestMaterializeFonts writes every embedded brand font into a target dir,
// asserts the returned dir path, and checks byte equality per file against
// the embedded content (TTF binaries and the license text).
func TestMaterializeFonts(t *testing.T) {
	names, err := fs.Glob(fontFS, "fonts/*")
	if err != nil {
		t.Fatalf("listing embedded fonts: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no embedded fonts found")
	}

	dir := filepath.Join(t.TempDir(), "fonts")
	gotDir, err := MaterializeFonts(dir)
	if err != nil {
		t.Fatalf("MaterializeFonts(%q) failed: %v", dir, err)
	}
	if gotDir != dir {
		t.Errorf("MaterializeFonts returned %q, want %q", gotDir, dir)
	}

	for _, name := range names {
		t.Run(filepath.Base(name), func(t *testing.T) {
			want, err := fontFS.ReadFile(name)
			if err != nil {
				t.Fatalf("reading embedded %s: %v", name, err)
			}
			got, err := os.ReadFile(filepath.Join(dir, filepath.Base(name)))
			if err != nil {
				t.Fatalf("materialized font %s missing: %v", filepath.Base(name), err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("materialized %s differs from embedded content: got %d bytes, want %d bytes",
					filepath.Base(name), len(got), len(want))
			}
		})
	}
}

// TestMaterializePackagesErrors exercises the MaterializePackages error paths:
// the underlying OS error from a failed mkdir/write must be propagated as-is,
// not swallowed.
func TestMaterializePackagesErrors(t *testing.T) {
	tests := []struct {
		name       string
		target     func(t *testing.T) string
		skipAsRoot bool // permission-based case: not exercisable when running as root
	}{
		{
			name: "target nested under a regular file",
			target: func(t *testing.T) string {
				blocker := filepath.Join(t.TempDir(), "blocker")
				if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
					t.Fatalf("creating blocker file: %v", err)
				}
				return filepath.Join(blocker, "out")
			},
		},
		{
			name:       "read-only target dir",
			skipAsRoot: true,
			target: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.Chmod(dir, 0o555); err != nil {
					t.Fatalf("chmod target dir: %v", err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(dir, 0o755); err != nil {
						t.Errorf("restoring target dir permissions: %v", err)
					}
				})
				return dir
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skipAsRoot && os.Geteuid() == 0 {
				t.Skip("permission-based error path is not exercisable as root")
			}
			err := MaterializePackages(tt.target(t))
			if err == nil {
				t.Fatal("MaterializePackages succeeded, want error")
			}
			var pathErr *os.PathError
			if !errors.As(err, &pathErr) {
				t.Errorf("expected propagated *os.PathError, got %T: %v", err, err)
			}
		})
	}
}

// TestVersionKeyDeterministic guards the cache version key: it must be a
// stable sha256 over the embedded trees so persistent caches keyed on it are
// invalidated exactly when embedded assets change.
func TestVersionKeyDeterministic(t *testing.T) {
	k1, err := VersionKey()
	if err != nil {
		t.Fatalf("VersionKey: %v", err)
	}
	k2, err := VersionKey()
	if err != nil {
		t.Fatalf("VersionKey (second call): %v", err)
	}
	if k1 != k2 {
		t.Errorf("VersionKey must be deterministic, got %q and %q", k1, k2)
	}
	if len(k1) != 64 {
		t.Errorf("expected 64-char sha256 hex, got %d chars: %q", len(k1), k1)
	}
	if k1 == "" {
		t.Fatal("expected non-empty version key")
	}
}

func TestMaterializeErrors(t *testing.T) {
	for _, materialize := range []struct {
		name string
		fn   func(string) error
	}{
		{name: "templates", fn: Materialize},
		{name: "fonts", fn: func(dir string) error {
			_, err := MaterializeFonts(dir)
			return err
		}},
	} {
		t.Run(materialize.name+"/mkdir", func(t *testing.T) {
			blocker := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(blocker, []byte("blocker"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := materialize.fn(filepath.Join(blocker, "nested")); err == nil {
				t.Fatal("materializer succeeded below a regular file")
			}
		})

		t.Run(materialize.name+"/write", func(t *testing.T) {
			dir := t.TempDir()
			file := "brief.typ"
			if materialize.name == "fonts" {
				file = "Inter-Regular.ttf"
			}
			if err := os.Mkdir(filepath.Join(dir, file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := materialize.fn(dir); err == nil {
				t.Fatal("materializer succeeded with a directory at an output file")
			}
		})
	}
}
