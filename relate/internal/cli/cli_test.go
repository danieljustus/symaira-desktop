package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHelp_HelpCmdShowsRootUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"help"})
	if code != 0 {
		t.Fatalf("help: code = %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "Usage: symrelate <command> [flags]") {
		t.Errorf("help output missing root usage line:\n%s", out)
	}
	if !strings.Contains(out, "Commands:") {
		t.Errorf("help output missing Commands section:\n%s", out)
	}
	if !strings.Contains(out, "symrelate help <command>") {
		t.Errorf("help output missing help hint:\n%s", out)
	}
}

func TestHelp_CmdWithLongHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"help", "import"})
	if code != 0 {
		t.Fatalf("help import: code = %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "Usage: symrelate import [flags]") {
		t.Errorf("help import missing usage line:\n%s", out)
	}
	if !strings.Contains(out, "vCard") {
		t.Errorf("help import missing long description:\n%s", out)
	}
	if !strings.Contains(out, "Examples:") {
		t.Errorf("help import missing examples header:\n%s", out)
	}
	if !strings.Contains(out, "symrelate import vcard") {
		t.Errorf("help import missing example content:\n%s", out)
	}
}

func TestHelp_CmdShortHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"help", "version"})
	if code != 0 {
		t.Fatalf("help version: code = %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "Usage: symrelate version [flags]") {
		t.Errorf("help version missing usage line:\n%s", out)
	}
	if !strings.Contains(out, "Print version information") {
		t.Errorf("help version missing short description:\n%s", out)
	}
}

func TestHelp_UnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"help", "bogus"})
	if code != 2 {
		t.Fatalf("help bogus: code = %d, want 2", code)
	}
	if !strings.Contains(stdout.String(), "unknown command") {
		t.Errorf("expected unknown command message, got: %s", stdout.String())
	}
}

func TestHelp_CmdDashHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"import", "--help"})
	if code != 0 {
		t.Fatalf("import --help: code = %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "Usage: symrelate import [flags]") {
		t.Errorf("import --help missing usage line:\n%s", out)
	}
	if !strings.Contains(out, "Examples:") {
		t.Errorf("import --help missing examples:\n%s", out)
	}
}

func TestHelp_CmdDashShortHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"import", "-h"})
	if code != 0 {
		t.Fatalf("import -h: code = %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "Usage: symrelate import [flags]") {
		t.Errorf("import -h missing usage line:\n%s", out)
	}
}

func TestRootUsage_HasHelpHint(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"--help"})
	if code != 0 {
		t.Fatalf("--help: code = %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "symrelate help <command>") {
		t.Errorf("root usage missing help hint:\n%s", out)
	}
}

func TestHelp_AllSeededCommandsHaveExamples(t *testing.T) {
	seeded := []string{"import", "backup", "console", "contact", "doctor"}
	for _, name := range seeded {
		cmd, ok := registry[name]
		if !ok {
			t.Errorf("seeded command %q not in registry", name)
			continue
		}
		if cmd.Examples == "" {
			t.Errorf("seeded command %q missing Examples", name)
		}
		if cmd.Long == "" {
			t.Errorf("seeded command %q missing Long", name)
		}
	}
}

func TestHelp_BackupExamples(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"help", "backup"})
	if code != 0 {
		t.Fatalf("help backup: code = %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "symrelate backup create") {
		t.Errorf("help backup missing create example:\n%s", out)
	}
	if !strings.Contains(out, "symrelate backup restore") {
		t.Errorf("help backup missing restore example:\n%s", out)
	}
}

func TestHelp_ContactExamples(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"help", "contact"})
	if code != 0 {
		t.Fatalf("help contact: code = %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "symrelate contact add") {
		t.Errorf("help contact missing add example:\n%s", out)
	}
}

func TestHelp_DoctorExamples(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), IO{Stdout: &stdout, Stderr: &stderr}, []string{"help", "doctor"})
	if code != 0 {
		t.Fatalf("help doctor: code = %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "symrelate doctor") {
		t.Errorf("help doctor missing example:\n%s", out)
	}
}
