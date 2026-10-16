package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundtripPreservesWorkingDirectory(t *testing.T) {
	for _, tc := range []struct {
		name  string
		vault string
		run   func(*testing.T)
	}{
		{"roundtrip", "vault", TestRoundtrip},
		{"bootstrap", "vault_bootstrap", TestLsPopulatesEmptySidecarFromExistingVaultFiles},
		{"stress", "vault_stress", TestEventsStress},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Exercise the actual tests in a disposable working directory, never
			// against the checkout's existing files, even before the fix.
			t.Chdir(t.TempDir())
			vault := filepath.Join("testdata", tc.vault)
			if err := os.MkdirAll(vault, 0o700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(vault, "preserve.txt")
			const original = "existing source-tree content\n"
			if err := os.WriteFile(sentinel, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}

			t.Run("execute", tc.run)

			//nolint:gosec // G304: sentinel is a fixed file in this subtest's private working directory.
			data, err := os.ReadFile(sentinel)
			if err != nil || string(data) != original {
				t.Errorf("roundtrip test changed existing working-directory content: %q, %v", data, err)
			}
			entries, err := os.ReadDir(vault)
			if err != nil || len(entries) != 1 || entries[0].Name() != "preserve.txt" {
				t.Errorf("roundtrip test polluted or removed the working-directory vault: %v, %v", entries, err)
			}
		})
	}
}
