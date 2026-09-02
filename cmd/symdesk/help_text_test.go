package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// retiredToolNamePattern matches the absorbed sibling binaries (issue #609
// and friends): symdesk swallowed symingest, symseek, symprint and symrelate,
// so none of their names should leak back into user-facing CLI help text.
var retiredToolNamePattern = regexp.MustCompile(`(?i)symingest|symseek|symprint|symrelate`)

// knownPendingHelpText lists command paths (and, for flags, "<path> --<flag>")
// whose help text still names a retired tool. These are pre-existing and
// tracked by other issues; #754 only reworded the mail/meetings/mail-poller
// strings it was scoped to. Remove an entry here once its text is cleaned up
// elsewhere so this test starts covering it too.
var knownPendingHelpText = map[string]bool{
	"symdesk export --profile":  true, // symprint profile terminology, tracked separately
	"symdesk export profiles":   true, // "List the symprint profiles..." Short text
	"symdesk paperless migrate": true, // Long text still names the symingest OCR pipeline, tracked separately
}

// TestHelpTextHasNoRetiredToolNames walks every command in the symdesk CLI
// tree and fails if a retired binary name (symingest, symseek, symprint,
// symrelate) shows up in its Short/Long/Example text or in a flag's usage
// string. This guards against regressions like the ones fixed in #754:
// cmd/symdesk/mail.go's "--config" flag used to describe its default as
// ".symingest.toml" and cmd/symdesk/meetings.go's participant subcommands
// used to call themselves "symrelate contact reference" lookups.
func TestHelpTextHasNoRetiredToolNames(t *testing.T) {
	root := newRootCmd()

	var walk func(cmd *cobra.Command, path string)
	walk = func(cmd *cobra.Command, path string) {
		full := strings.TrimSpace(path + " " + cmd.Name())

		for label, text := range map[string]string{"Short": cmd.Short, "Long": cmd.Long, "Example": cmd.Example} {
			if text == "" {
				continue
			}
			if retiredToolNamePattern.MatchString(text) {
				if knownPendingHelpText[full] {
					continue
				}
				t.Errorf("command %q %s text references a retired tool name: %q", full, label, text)
			}
		}

		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if !retiredToolNamePattern.MatchString(f.Usage) {
				return
			}
			key := full + " --" + f.Name
			if knownPendingHelpText[key] {
				return
			}
			t.Errorf("command %q flag --%s usage references a retired tool name: %q", full, f.Name, f.Usage)
		})

		for _, sub := range cmd.Commands() {
			walk(sub, full)
		}
	}

	walk(root, "")
}

// TestMailPollerLogHasNoRetiredToolNames guards the mail poller's log
// messages, which are user-facing (they point developers at a doctor
// command) but live outside the cobra command tree, so the help-text walk
// above cannot see them. Fixed in #754: they used to say "run 'symingest
// doctor'" even though only `symdesk doctor` exists. This greps the actual
// log.Printf format strings in the poller's source so a future regression
// (a new or edited log line naming a retired tool) fails here too.
func TestMailPollerLogHasNoRetiredToolNames(t *testing.T) {
	pollerSrc := filepath.Join("..", "..", "internal", "ingest", "internal", "ingest", "mail.go")
	src, err := os.ReadFile(pollerSrc) //nolint:gosec // fixed, repo-relative test path; not user input
	if err != nil {
		t.Fatalf("read mail poller source %s: %v", pollerSrc, err)
	}

	logCallPattern := regexp.MustCompile(`log\.Printf\("([^"]*)"`)
	matches := logCallPattern.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatalf("found no log.Printf calls in %s; the pattern below may need updating", pollerSrc)
	}
	for _, m := range matches {
		msg := m[1]
		if retiredToolNamePattern.MatchString(msg) {
			t.Errorf("mail poller log message in %s references a retired tool name: %q", pollerSrc, msg)
		}
	}
}
