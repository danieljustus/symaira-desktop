package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderPDFInvokesSymprintWithInputOutputAndProfile(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	stdinPath := filepath.Join(dir, "stdin.md")
	outputPath := filepath.Join(dir, "nested", "export.pdf")
	writeMockTool(t, dir, "symprint", `#!/bin/bash
if [ "$1" != "render" ]; then
  echo "unexpected command: $*" >&2
  exit 2
fi
printf '%s\n' "$@" > "$SYMDESK_TEST_ARGS"
cat > "$SYMDESK_TEST_STDIN"
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    printf 'mock pdf' > "$1"
    exit 0
  fi
  shift
done
echo "missing output path" >&2
exit 3
`)
	withMockPath(t, dir)
	t.Setenv("SYMDESK_TEST_ARGS", argsPath)
	t.Setenv("SYMDESK_TEST_STDIN", stdinPath)

	got, err := RenderPDF([]byte("# Export\n\nBody\n"), outputPath, "behoerde")
	if err != nil {
		t.Fatalf("RenderPDF failed: %v", err)
	}
	if got != outputPath {
		t.Fatalf("expected output path %q, got %q", outputPath, got)
	}
	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("expected mock output: %v", err)
	}
	if string(output) != "mock pdf" {
		t.Errorf("unexpected PDF content %q", output)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(args)) != strings.Join([]string{"render", "-", "-o", outputPath, "-p", "behoerde"}, "\n") {
		t.Errorf("unexpected symprint arguments:\n%s", args)
	}
	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != "# Export\n\nBody\n" {
		t.Errorf("unexpected symprint stdin %q", stdin)
	}
}

func TestRenderPDFReportsMissingAndFailingSymprint(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if _, err := RenderPDF([]byte("body"), filepath.Join(t.TempDir(), "out.pdf"), ""); err == nil || !strings.Contains(err.Error(), "symprint not found") {
			t.Fatalf("expected missing symprint error, got %v", err)
		}
	})

	t.Run("failing", func(t *testing.T) {
		dir := t.TempDir()
		writeMockTool(t, dir, "symprint", `#!/bin/bash
echo "renderer failed" >&2
exit 7
`)
		withMockPath(t, dir)
		if _, err := RenderPDF([]byte("body"), filepath.Join(t.TempDir(), "out.pdf"), ""); err == nil || !strings.Contains(err.Error(), "symprint render failed") || !strings.Contains(err.Error(), "renderer failed") {
			t.Fatalf("expected wrapped renderer error, got %v", err)
		}
	})
}

func TestListSymprintProfilesHandlesSuccessFailureAndMalformedOutput(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symprint", `#!/bin/bash
if [ "$1" != "profiles" ] || [ "$2" != "--json" ]; then
  echo "unexpected command: $*" >&2
  exit 2
fi
case "$SYMDESK_SYMPRINT_MODE" in
  fail)
    echo "profile lookup failed" >&2
    exit 3
    ;;
  malformed)
    echo "not json"
    ;;
  *)
    echo '[{"name":"brief"},{"name":""},{"name":"report"}]'
    ;;
esac
`)
	withMockPath(t, dir)

	profiles, err := ListSymprintProfiles()
	if err != nil {
		t.Fatalf("ListSymprintProfiles failed: %v", err)
	}
	if got, want := strings.Join(profiles, ","), "brief,report"; got != want {
		t.Errorf("expected %q, got %q", want, got)
	}

	t.Setenv("SYMDESK_SYMPRINT_MODE", "fail")
	if _, err := ListSymprintProfiles(); err == nil || !strings.Contains(err.Error(), "profile lookup failed") {
		t.Fatalf("expected wrapped profile lookup failure, got %v", err)
	}

	t.Setenv("SYMDESK_SYMPRINT_MODE", "malformed")
	if _, err := ListSymprintProfiles(); err == nil || !strings.Contains(err.Error(), "unmarshal") {
		t.Fatalf("expected malformed JSON error, got %v", err)
	}
}
