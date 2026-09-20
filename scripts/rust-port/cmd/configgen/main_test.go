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
// $HOME/.config when filepath.IsAbs rejects XDG_CONFIG_HOME, and Windows rejects
// both "/fixture/config" and "\fixture\config" — only a drive-qualified path
// counts. Cases are therefore applied in the host's syntax and the results are
// canonicalized back, so the fixture stays byte-identical everywhere.
func TestFixturePathTranslationRoundTrips(t *testing.T) {
	const windowsRoot = `C:\fixture`
	for _, tc := range []struct{ canonical, host, resolved string }{
		{canonical: "/fixture/home", host: windowsRoot + `\home`, resolved: windowsRoot + `\home`},
		{canonical: "  /fixture/data  ", host: "  " + windowsRoot + `\data  `, resolved: windowsRoot + `\data`},
		{canonical: "/fixture/config", host: windowsRoot + `\config`, resolved: windowsRoot + `\config`},
	} {
		if got := hostPathWith(tc.canonical, windowsRoot); got != tc.host {
			t.Fatalf("hostPathWith(%q, %q) = %q, want %q", tc.canonical, windowsRoot, got, tc.host)
		}
		if got := canonicalPathWith(tc.resolved, windowsRoot); got != strings.TrimSpace(tc.canonical) {
			t.Fatalf("canonicalPathWith(%q, %q) = %q, want %q", tc.resolved, windowsRoot, got, strings.TrimSpace(tc.canonical))
		}
	}
	if got := canonicalPathWith(windowsRoot+`\home\.config\symdesk\config.toml`, windowsRoot); got != "/fixture/home/.config/symdesk/config.toml" {
		t.Fatalf("canonicalPathWith() = %q, want the canonical global path", got)
	}
	for _, value := range []string{"/fixture/home", "  /fixture/data  ", "relative/config"} {
		if got := hostPathWith(value, fixtureRoot); got != value {
			t.Fatalf("hostPathWith(%q, %q) = %q, want the value unchanged", value, fixtureRoot, got)
		}
		if got := canonicalPathWith(value, fixtureRoot); got != value {
			t.Fatalf("canonicalPathWith(%q, %q) = %q, want the value unchanged", value, fixtureRoot, got)
		}
	}
	if got := hostPathWith("relative/config", windowsRoot); got != "relative/config" {
		t.Fatalf("hostPathWith() rewrote a relative value: %q", got)
	}
	if got := canonicalPathWith("relative/config", windowsRoot); got != "relative/config" {
		t.Fatalf("canonicalPathWith() rewrote a relative value: %q", got)
	}
}

func TestHostEnvironmentKeepsCanonicalPathsOnPosixHosts(t *testing.T) {
	if hostRoot() != fixtureRoot {
		t.Skip("the host does not use the canonical root, so the identity expectation does not apply")
	}
	canonical := map[string]string{
		"HOME":            "/fixture/home",
		"XDG_DATA_HOME":   "  /fixture/data  ",
		"XDG_CONFIG_HOME": "relative/config",
		"WINDOWS_STYLE":   `C:\fixture\home`,
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
	if !strings.Contains(report, "recorded=") || !strings.Contains(report, "checked=") {
		t.Fatalf("firstDifference() = %q, want both windows in the report", report)
	}
}
