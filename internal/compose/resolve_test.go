package compose

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeExecutable writes an executable stub at dir/name so Resolve finds it.
func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil { //nolint:gosec // test fixture directory under t.TempDir()
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/bash\nexit 0\n"), 0755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	return path
}

// isolateEnv gives the test its own $HOME (so the real machine's
// ~/.symaira/bin, if any, never leaks into the test) and clears
// $SYMAIRA_BIN and $PATH so only sources the test explicitly wires up are
// visible to Resolve.
func isolateEnv(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(SymairaBinEnvVar, "")
	// An empty-but-real temp dir, rather than an empty PATH string: an
	// empty PATH entry means "search the current directory" on POSIX,
	// which would make this test depend on the working directory.
	t.Setenv("PATH", t.TempDir())
	return home
}

func TestResolveOrderAcrossThreeSources(t *testing.T) {
	t.Run("symaira_bin env var wins when set", func(t *testing.T) {
		home := isolateEnv(t)
		symairaBinDir := filepath.Join(t.TempDir(), "envbin")
		writeExecutable(t, symairaBinDir, "symseek")
		t.Setenv(SymairaBinEnvVar, symairaBinDir)

		// Also plant the tool in the default managed-runtime dir and on
		// PATH, to prove SYMAIRA_BIN takes priority over both.
		managedDir := filepath.Join(home, ".symaira", "bin")
		writeExecutable(t, managedDir, "symseek")
		pathDir := t.TempDir()
		writeExecutable(t, pathDir, "symseek")
		t.Setenv("PATH", pathDir)

		path, origin, err := ResolveWithOrigin("symseek")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if origin != OriginSymairaBinEnv {
			t.Errorf("expected origin %q, got %q", OriginSymairaBinEnv, origin)
		}
		if want := filepath.Join(symairaBinDir, "symseek"); path != want {
			t.Errorf("expected path %q, got %q", want, path)
		}
	})

	t.Run("managed runtime dir used when SYMAIRA_BIN unset", func(t *testing.T) {
		home := isolateEnv(t)
		managedDir := filepath.Join(home, ".symaira", "bin")
		writeExecutable(t, managedDir, "symmemory")

		// Also plant on PATH to prove the managed runtime dir wins over PATH.
		pathDir := t.TempDir()
		writeExecutable(t, pathDir, "symmemory")
		t.Setenv("PATH", pathDir)

		path, origin, err := ResolveWithOrigin("symmemory")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if origin != OriginManagedRuntime {
			t.Errorf("expected origin %q, got %q", OriginManagedRuntime, origin)
		}
		if want := filepath.Join(managedDir, "symmemory"); path != want {
			t.Errorf("expected path %q, got %q", want, path)
		}
	})

	t.Run("falls back to PATH when neither override exists", func(t *testing.T) {
		isolateEnv(t)
		pathDir := t.TempDir()
		writeExecutable(t, pathDir, "symprint")
		t.Setenv("PATH", pathDir)

		path, origin, err := ResolveWithOrigin("symprint")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if origin != OriginPath {
			t.Errorf("expected origin %q, got %q", OriginPath, origin)
		}
		if want := filepath.Join(pathDir, "symprint"); path != want {
			t.Errorf("expected path %q, got %q", want, path)
		}
	})

	t.Run("not found anywhere returns an error", func(t *testing.T) {
		isolateEnv(t)
		if _, _, err := ResolveWithOrigin("symdoesnotexist"); err == nil {
			t.Fatal("expected an error when the binary is absent from all three sources")
		}
	})

	t.Run("SYMAIRA_BIN set but missing the binary falls through to managed runtime dir", func(t *testing.T) {
		home := isolateEnv(t)
		emptyEnvDir := t.TempDir()
		t.Setenv(SymairaBinEnvVar, emptyEnvDir)

		managedDir := filepath.Join(home, ".symaira", "bin")
		writeExecutable(t, managedDir, "symbrowse")

		path, origin, err := ResolveWithOrigin("symbrowse")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if origin != OriginManagedRuntime {
			t.Errorf("expected origin %q, got %q", OriginManagedRuntime, origin)
		}
		if want := filepath.Join(managedDir, "symbrowse"); path != want {
			t.Errorf("expected path %q, got %q", want, path)
		}
	})

	t.Run("a name containing a path separator bypasses the managed-runtime tiers", func(t *testing.T) {
		isolateEnv(t)
		dir := t.TempDir()
		abs := writeExecutable(t, dir, "custom-tool")

		path, origin, err := ResolveWithOrigin(abs)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if origin != OriginPath {
			t.Errorf("expected origin %q for an absolute-path name, got %q", OriginPath, origin)
		}
		if path != abs {
			t.Errorf("expected path %q, got %q", abs, path)
		}
	})

	t.Run("Resolve returns just the path", func(t *testing.T) {
		isolateEnv(t)
		pathDir := t.TempDir()
		writeExecutable(t, pathDir, "symtestbin")
		t.Setenv("PATH", pathDir)

		path, err := Resolve("symtestbin")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join(pathDir, "symtestbin"); path != want {
			t.Errorf("expected path %q, got %q", want, path)
		}
	})
}

func TestManagedRuntimeDirExists(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if ManagedRuntimeDirExists() {
			t.Error("expected false when ~/.symaira/bin does not exist")
		}
	})

	t.Run("present", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if err := os.MkdirAll(filepath.Join(home, ".symaira", "bin"), 0755); err != nil { //nolint:gosec // test fixture directory under t.TempDir()
			t.Fatal(err)
		}
		if !ManagedRuntimeDirExists() {
			t.Error("expected true when ~/.symaira/bin exists")
		}
	})
}

func TestManagedRuntimeDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed runtime dir layout is POSIX-specific")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := ManagedRuntimeDir()
	want := filepath.Join(home, ".symaira", "bin")
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestInspectToolReportsManagedAndPATHCopies(t *testing.T) {
	home := isolateEnv(t)
	managedDir := filepath.Join(home, ".symaira", "bin")
	if err := os.MkdirAll(managedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	writeMockTool(t, managedDir, "symmemory", `#!/bin/sh
printf '%s\n' '{"tool":"symmemory","version":"1.2.0","schema_version":1}'
`)
	writeMockTool(t, pathDir, "symmemory", `#!/bin/sh
printf '%s\n' '{"tool":"symmemory","version":"2.0.0","schema_version":1}'
`)
	t.Setenv("PATH", pathDir)

	got := InspectTool("symmemory")
	if !got.Managed.Found || got.Managed.Version != "1.2.0" {
		t.Errorf("managed installation = %+v, want found version 1.2.0", got.Managed)
	}
	if !got.PATH.Found || got.PATH.Version != "2.0.0" {
		t.Errorf("PATH installation = %+v, want found version 2.0.0", got.PATH)
	}
	if got.Origin != OriginManagedRuntime {
		t.Errorf("effective origin = %q, want %q", got.Origin, OriginManagedRuntime)
	}
	if got.Effective.Path != got.Managed.Path || got.Effective.Version != got.Managed.Version {
		t.Errorf("effective installation = %+v, want managed installation", got.Effective)
	}
}

func TestInspectToolAbsentIsNotAnError(t *testing.T) {
	isolateEnv(t)

	got := InspectTool("symdoesnotexist")
	if got.Effective.Found || got.Managed.Found || got.PATH.Found {
		t.Errorf("absent tool provenance = %+v, want all installations absent", got)
	}
	if got.Origin != "" {
		t.Errorf("absent tool origin = %q, want empty", got.Origin)
	}
}
