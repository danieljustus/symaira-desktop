package inventory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("SYMDESK_INVENTORY_GIT_TIMEOUT_HELPER") == "1" {
		_, _ = os.Stderr.WriteString("inventory git deadline sentinel\n")
		time.Sleep(time.Minute)
		os.Exit(23)
	}
	os.Exit(m.Run())
}

func TestInventoryGitOutputPreservesDeadlineAndProcessError(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYMDESK_INVENTORY_GIT_TIMEOUT_HELPER", "1")
	output, err := inventoryGitOutputWithExecutable(t.TempDir(), executable, "checkout", "--detach", "deadline-control")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected preserved deadline cause, got %v", err)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("expected preserved process error, got %v", err)
	}
	if !strings.Contains(err.Error(), "git checkout --detach deadline-control:") ||
		!strings.Contains(err.Error(), "inventory git deadline sentinel") || output != nil {
		t.Fatalf("expected command, captured output and nil result, got %q, %v", output, err)
	}
}
