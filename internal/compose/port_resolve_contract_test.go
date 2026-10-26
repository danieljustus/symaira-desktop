package compose

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type portResolveFile struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Target string `json:"target"`
}
type portResolveCase struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Home          string            `json:"home"`
	Bin           string            `json:"bin"`
	PATH          string            `json:"path_env"`
	ErrDot        string            `json:"execerrdot"`
	Files         []portResolveFile `json:"files"`
	Path          string            `json:"path"`
	Origin        string            `json:"origin"`
	Error         string            `json:"error"`
	Managed       string            `json:"managed"`
	ManagedExists bool              `json:"managed_exists"`
}

// This oracle never starts a discovered executable: it exercises the real Go
// resolver against an isolated layout. {root} replaces only the disposable root.
func TestPortComposeResolveContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix exec.LookPath corpus; Windows PATHEXT oracle remains open")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	oracleCommit := fixtureoracle.Current().Commit
	source, err := os.ReadFile(filepath.Join(repo, "internal/compose/resolve.go"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(source)
	file := func(path string, mode uint32) portResolveFile {
		return portResolveFile{Path: path, Kind: "file", Mode: mode}
	}
	exe := func(path string) portResolveFile { return file(path, 0755) }
	dir := func(path string) portResolveFile { return portResolveFile{Path: path, Kind: "dir", Mode: 0755} }
	link := func(path, target string) portResolveFile {
		return portResolveFile{Path: path, Kind: "symlink", Target: target}
	}
	base := func(id string, files ...portResolveFile) portResolveCase {
		return portResolveCase{ID: id, Name: "symtest", Home: "{root}/home", Bin: "{root}/env", PATH: "{root}/path", Files: files}
	}
	cases := []portResolveCase{
		base("missing"), base("env", exe("env/symtest")), base("managed", exe("home/.symaira/bin/symtest")), base("path", exe("path/symtest")),
		base("env-wins", exe("env/symtest"), exe("home/.symaira/bin/symtest"), exe("path/symtest")),
		base("managed-beats-path", exe("home/.symaira/bin/symtest"), exe("path/symtest")),
		base("nonexecutable-env-falls-through", file("env/symtest", 0644), exe("path/symtest")),
		base("directory-env-falls-through", dir("env/symtest"), exe("path/symtest")),
		base("nonexecutable-managed-falls-through", file("home/.symaira/bin/symtest", 0644), exe("path/symtest")),
		base("directory-path-not-executable", dir("path/symtest")), base("nonexecutable-path-not-found", file("path/symtest", 0644)),
		base("env-symlink", exe("target"), link("env/symtest", "{root}/target")),
		base("dangling-env-symlink-falls-through", link("env/symtest", "{root}/absent"), exe("path/symtest")),
		base("path-symlink", exe("target"), link("path/symtest", "{root}/target")),
		base("executable-owner-only", file("env/symtest", 0100)), base("executable-group-only-env", file("env/symtest", 0010)),
		base("managed-directory-exists", dir("home/.symaira/bin")), base("managed-is-file", file("home/.symaira/bin", 0644)),
	}
	add := func(id, name, home, bin, path, errdot string, files ...portResolveFile) {
		cases = append(cases, portResolveCase{ID: id, Name: name, Home: home, Bin: bin, PATH: path, ErrDot: errdot, Files: files})
	}
	add("explicit-absolute", "{root}/direct", "{root}/home", "{root}/env", "{root}/path", "", exe("direct"), exe("env/symtest"))
	add("explicit-relative", "./direct", "{root}/home", "{root}/env", "", "", exe("direct"))
	add("explicit-missing", "./absent", "{root}/home", "", "", "")
	add("explicit-nonexecutable", "./direct", "{root}/home", "", "", "", file("direct", 0644))
	add("explicit-directory", "./direct", "{root}/home", "", "", "", dir("direct"))
	add("explicit-subpath-bypasses-managed", "sub/symtest", "{root}/home", "{root}/env", "{root}/path", "", exe("env/symtest"), exe("path/symtest"))
	add("empty-path-does-not-search-cwd", "symtest", "", "", "", "", exe("symtest"))
	add("dot-path-rejected", "symtest", "", "", ".", "", exe("symtest"))
	add("empty-path-component-rejected", "symtest", "", "", ":{root}/path", "", exe("symtest"), exe("path/symtest"))
	add("relative-path-rejected", "symtest", "", "", "relative", "", exe("relative/symtest"))
	add("dot-path-explicitly-enabled", "symtest", "", "", ".", "0", exe("symtest"))
	add("relative-env-allowed", "symtest", "", "relative", "", "", exe("relative/symtest"))
	add("empty-name", "", "", "{root}/env", "{root}/path", "")
	add("dot-name", ".", "", "{root}/env", "{root}/path", "")
	add("dotdot-name", "..", "", "{root}/env", "{root}/path", "")
	add("path-order", "symtest", "", "", "{root}/second:{root}/first", "", exe("first/symtest"), exe("second/symtest"))
	add("path-skips-bad-entry", "symtest", "", "", "{root}/first:{root}/second", "", file("first/symtest", 0644), exe("second/symtest"))
	add("clean-env-path", "symtest", "", "{root}/env/../env//", "", "", exe("env/symtest"))
	add("unset-home-missing", "symtest", "", "", "", "")
	add("quoted-name", "sym\"test", "", "", "", "")
	for i := range cases {
		c := &cases[i]
		root := t.TempDir()
		expand := func(s string) string { return strings.ReplaceAll(s, "{root}", root) }
		for _, f := range c.Files {
			path := filepath.Join(root, f.Path)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			switch f.Kind {
			case "dir":
				err = os.MkdirAll(path, os.FileMode(f.Mode))
			case "symlink":
				err = os.Symlink(expand(f.Target), path)
			default:
				err = os.WriteFile(path, []byte("#!/bin/sh\nexit 99\n"), os.FileMode(f.Mode))
				if err == nil {
					err = os.Chmod(path, os.FileMode(f.Mode))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		func() {
			t.Chdir(root)
			t.Setenv("HOME", expand(c.Home))
			t.Setenv("USERPROFILE", expand(c.Home))
			t.Setenv("SYMAIRA_BIN", expand(c.Bin))
			t.Setenv("PATH", expand(c.PATH))
			t.Setenv("GODEBUG", "execerrdot="+c.ErrDot)
			path, origin, resolveErr := ResolveWithOrigin(expand(c.Name))
			normalize := func(s string) string { return strings.ReplaceAll(s, root, "{root}") }
			c.Path = normalize(path)
			c.Origin = string(origin)
			if resolveErr != nil {
				c.Error = normalize(resolveErr.Error())
			}
			c.Managed = normalize(ManagedRuntimeDir())
			c.ManagedExists = ManagedRuntimeDirExists()
		}()
	}
	document := struct {
		SchemaVersion int               `json:"schema_version"`
		OracleCommit  string            `json:"oracle_commit"`
		Platform      string            `json:"platform"`
		SourceHashes  map[string]string `json:"source_hashes"`
		Cases         []portResolveCase `json:"cases"`
	}{1, oracleCommit, "unix", map[string]string{"internal/compose/resolve.go": hex.EncodeToString(sum[:])}, cases}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(repo, "testdata/port/ai/compose-resolve.json")
	if override := os.Getenv("PORT_FIXTURE_PATH"); override != "" {
		path = override
	}
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	} else {
		expected, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(expected, data) {
			t.Fatal("compose resolve fixture drift; regenerate deliberately")
		}
	}
	t.Logf("executed %d real Go resolver cases", len(cases))
}
