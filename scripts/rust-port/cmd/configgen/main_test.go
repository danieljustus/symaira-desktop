package main

import (
	"os"
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
