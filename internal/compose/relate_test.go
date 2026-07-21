package compose

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeMockSymrelate(t *testing.T, dir, script string) {
	t.Helper()
	path := filepath.Join(dir, "symrelate")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
}

func withBarePATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", "/usr/bin:/bin")
	ResetCache()
	t.Cleanup(ResetCache)
}

const mockSymrelateHappyScript = `#!/bin/bash
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  echo '{"tool":"symrelate","version":"0.1.1","schema_version":5,"api_version":"v1"}'
  exit 0
fi
if [ "$1" = "contact" ] && [ "$2" = "ref" ]; then
  case "$3" in
    c-ada)
      echo '{"provider":"symrelate","schema_version":1,"id":"c-ada","kind":"person","display_name":"Ada Lovelace","future_flag":true}'
      exit 0 ;;
    c-org)
      echo '{"provider":"symrelate","schema_version":1,"id":"c-org","kind":"organization","display_name":"Analytical Engines Ltd"}'
      exit 0 ;;
    *)
      echo 'symrelate: contact.GetRef: contact not found' >&2
      exit 1 ;;
  esac
fi
`

func TestResolveContactRefHappyPath(t *testing.T) {
	dir := t.TempDir()
	writeMockSymrelate(t, dir, mockSymrelateHappyScript)
	withMockPath(t, dir)

	ref, err := ResolveContactRef("c-ada")
	if err != nil {
		t.Fatalf("ResolveContactRef() error = %v", err)
	}
	if ref.Provider != ContactRefProvider || ref.SchemaVersion != 1 || ref.ID != "c-ada" || ref.Kind != "person" || ref.DisplayName != "Ada Lovelace" {
		t.Errorf("unexpected ref: %+v", ref)
	}
	// Unknown additive fields round-trip (forward compatibility), …
	if ref.Extras["future_flag"] != true {
		t.Errorf("expected unknown field future_flag preserved, got %+v", ref.Extras)
	}
	// … but known contract keys never leak into Extras.
	for _, k := range []string{"provider", "schema_version", "id", "kind", "display_name"} {
		if _, dup := ref.Extras[k]; dup {
			t.Errorf("known key %q must not appear in Extras", k)
		}
	}
}

func TestResolveContactRefOrganization(t *testing.T) {
	dir := t.TempDir()
	writeMockSymrelate(t, dir, mockSymrelateHappyScript)
	withMockPath(t, dir)

	ref, err := ResolveContactRef("c-org")
	if err != nil {
		t.Fatalf("ResolveContactRef() error = %v", err)
	}
	if ref.Kind != "organization" || ref.DisplayName != "Analytical Engines Ltd" {
		t.Errorf("unexpected org ref: %+v", ref)
	}
}

func TestResolveContactRefUnavailable(t *testing.T) {
	withBarePATH(t)

	if _, err := ResolveContactRef("c-ada"); err == nil {
		t.Fatal("expected an error when symrelate is absent from PATH")
	}
}

func TestResolveContactRefNotFound(t *testing.T) {
	dir := t.TempDir()
	writeMockSymrelate(t, dir, mockSymrelateHappyScript)
	withMockPath(t, dir)

	_, err := ResolveContactRef("c-erased")
	if !errors.Is(err, ErrContactNotFound) {
		t.Fatalf("expected ErrContactNotFound, got %v", err)
	}
}

func TestResolveContactRefIncompatibleProvider(t *testing.T) {
	dir := t.TempDir()
	writeMockSymrelate(t, dir, `#!/bin/bash
if [ "$1" = "contact" ] && [ "$2" = "ref" ]; then
  echo '{"provider":"not-symrelate","schema_version":1,"id":"c-ada","kind":"person","display_name":"Ada"}'
  exit 0
fi
`)
	withMockPath(t, dir)

	_, err := ResolveContactRef("c-ada")
	if !errors.Is(err, ErrContactRefIncompatible) {
		t.Fatalf("expected ErrContactRefIncompatible, got %v", err)
	}
}

func TestResolveContactRefIncompatibleSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	writeMockSymrelate(t, dir, `#!/bin/bash
if [ "$1" = "contact" ] && [ "$2" = "ref" ]; then
  echo '{"provider":"symrelate","schema_version":2,"id":"c-ada","kind":"person","display_name":"Ada"}'
  exit 0
fi
`)
	withMockPath(t, dir)

	_, err := ResolveContactRef("c-ada")
	if !errors.Is(err, ErrContactRefIncompatible) {
		t.Fatalf("expected ErrContactRefIncompatible for schema_version 2, got %v", err)
	}
}

func TestResolveContactRefMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	writeMockSymrelate(t, dir, `#!/bin/bash
if [ "$1" = "contact" ] && [ "$2" = "ref" ]; then
  echo 'this is not json'
  exit 0
fi
`)
	withMockPath(t, dir)

	if _, err := ResolveContactRef("c-ada"); err == nil {
		t.Fatal("expected a decode error for malformed JSON")
	}
}

func TestResolveContactRefRejectsIDMismatch(t *testing.T) {
	dir := t.TempDir()
	writeMockSymrelate(t, dir, `#!/bin/bash
if [ "$1" = "contact" ] && [ "$2" = "ref" ]; then
  echo '{"provider":"symrelate","schema_version":1,"id":"c-someone-else","kind":"person","display_name":"Ada"}'
  exit 0
fi
`)
	withMockPath(t, dir)

	if _, err := ResolveContactRef("c-ada"); err == nil {
		t.Fatal("expected an error when the resolved ID does not match the requested one")
	}
}

// A buggy or hostile symrelate could smuggle contact points, notes or
// local paths into unknown additive fields; those keys must be dropped
// before the reference is allowed anywhere near vault metadata, while
// benign unknown fields still round-trip.
func TestResolveContactRefSanitizesPrivateExtras(t *testing.T) {
	dir := t.TempDir()
	writeMockSymrelate(t, dir, `#!/bin/bash
if [ "$1" = "contact" ] && [ "$2" = "ref" ]; then
  echo '{"provider":"symrelate","schema_version":1,"id":"c-ada","kind":"person","display_name":"Ada Lovelace","future_flag":true,"email":"ada@private.example","phone":"+1 555 000","address":"12 Secret Lane","notes":"private notes","transcript_path":"/Users/ada/private/t.md","avatar_color":"gold"}'
  exit 0
fi
`)
	withMockPath(t, dir)

	ref, err := ResolveContactRef("c-ada")
	if err != nil {
		t.Fatalf("ResolveContactRef() error = %v", err)
	}
	for _, banned := range []string{"email", "phone", "address", "notes", "transcript_path"} {
		if _, found := ref.Extras[banned]; found {
			t.Errorf("private-looking key %q must be dropped, got %+v", banned, ref.Extras)
		}
	}
	if ref.Extras["future_flag"] != true || ref.Extras["avatar_color"] != "gold" {
		t.Errorf("benign unknown fields must be preserved, got %+v", ref.Extras)
	}
}

func TestResolveContactRefBoundedTimeout(t *testing.T) {
	dir := t.TempDir()
	writeMockSymrelate(t, dir, `#!/bin/bash
if [ "$1" = "contact" ] && [ "$2" = "ref" ]; then
  sleep 30
fi
`)
	withMockPath(t, dir)

	old := symrelateCallTimeout
	symrelateCallTimeout = 200 * time.Millisecond
	t.Cleanup(func() { symrelateCallTimeout = old })

	start := time.Now()
	_, err := ResolveContactRef("c-ada")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("call was not bounded: took %v", elapsed)
	}
}
