package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The corpus clears every environment name it tracks before each case, so a case
// that pins only HOME leaves Windows without a home directory: Windows resolves
// it from USERPROFILE, and the corpus failed there with
// "user home dir: %userprofile% is not defined". A case environment therefore
// has to carry both names, describing the same directory on every platform.
func TestWithEnvironmentMirrorsHomeNames(t *testing.T) {
	t.Setenv("HOME", "/outer/home")
	t.Setenv("USERPROFILE", "/outer/profile")
	for _, tc := range []struct {
		name        string
		values      map[string]string
		wantHome    string
		wantProfile string
	}{
		{name: "posix case gains the Windows name", values: map[string]string{"HOME": "/fixture/home"}, wantHome: "/fixture/home", wantProfile: "/fixture/home"},
		{name: "windows case gains the POSIX name", values: map[string]string{"USERPROFILE": "/fixture/profile"}, wantHome: "/fixture/profile", wantProfile: "/fixture/profile"},
		{name: "an explicit pair is kept", values: map[string]string{"HOME": "/a", "USERPROFILE": "/b"}, wantHome: "/a", wantProfile: "/b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := withEnvironment(tc.values, func() error {
				if got := os.Getenv("HOME"); got != tc.wantHome {
					t.Fatalf("HOME inside the case = %q, want %q", got, tc.wantHome)
				}
				if got := os.Getenv("USERPROFILE"); got != tc.wantProfile {
					t.Fatalf("USERPROFILE inside the case = %q, want %q", got, tc.wantProfile)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("withEnvironment() error = %v", err)
			}
		})
	}
	if got := os.Getenv("HOME"); got != "/outer/home" {
		t.Fatalf("HOME after the cases = %q, want the caller's value restored", got)
	}
	if got := os.Getenv("USERPROFILE"); got != "/outer/profile" {
		t.Fatalf("USERPROFILE after the cases = %q, want the caller's value restored", got)
	}
}

// The corpus records canonical fixture paths and applies them to the host, which
// must resolve them the same way on every leg: configkit falls back to
// $HOME/.config when XDG_CONFIG_HOME is not absolute, and Windows does not treat
// "/fixture/config" as absolute. The translation has to leave the canonical
// values alone wherever the host already uses them.
func TestHostEnvironmentKeepsCanonicalPathsOnPosixHosts(t *testing.T) {
	canonical := map[string]string{
		"HOME":            "/fixture/home",
		"XDG_DATA_HOME":   "  /fixture/data  ",
		"XDG_CONFIG_HOME": "relative/config",
		"WINDOWS_STYLE":   "C:\\fixture\\home",
	}
	got := hostEnvironment(canonical)
	for key, want := range canonical {
		if got[key] != want {
			t.Fatalf("hostEnvironment()[%q] = %q, want %q", key, got[key], want)
		}
	}
	if _, ok := got["MISSING"]; ok {
		t.Fatalf("hostEnvironment() invented a key: %#v", got)
	}
	if filepath.Separator != '/' {
		t.Skip("host separator is not POSIX; the identity expectation does not apply")
	}
	if hostPath("/fixture/home") != "/fixture/home" {
		t.Fatalf("hostPath() changed a canonical path: %q", hostPath("/fixture/home"))
	}
	if hostPath("  /fixture/data  ") != "  /fixture/data  " {
		t.Fatalf("hostPath() dropped the padding a case exercises: %q", hostPath("  /fixture/data  "))
	}
	if hostPath("relative/config") != "relative/config" {
		t.Fatalf("hostPath() rewrote a relative value: %q", hostPath("relative/config"))
	}
}

func TestFirstDifferenceNamesTheOffset(t *testing.T) {
	recorded := []byte(`{"data_home": "/fixture/data"}`)
	checked := []byte(`{"data_home": "\fixture\data"}`)
	report := firstDifference(recorded, checked)
	if !strings.Contains(report, "byte 15") {
		t.Fatalf("firstDifference() = %q, want the offset of the first differing byte", report)
	}
	if !strings.Contains(report, `\fixture\data`) {
		t.Fatalf("firstDifference() = %q, want the checked window in the report", report)
	}
	if !strings.Contains(report, "/fixture/data") {
		t.Fatalf("firstDifference() = %q, want the recorded window in the report", report)
	}
}
