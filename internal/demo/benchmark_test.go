package demo

import (
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

// BenchmarkLargeVaultIndexAndSearch is intentionally opt-in: the 10k fixture
// is a release-performance measurement, not a unit-test workload.
func BenchmarkLargeVaultIndexAndSearch(b *testing.B) {
	vaultDir := filepath.Join(b.TempDir(), "vault")
	if err := InitLarge(vaultDir); err != nil {
		b.Fatal(err)
	}

	for i := 0; i < b.N; i++ {
		db, err := sidecar.Open(filepath.Join(b.TempDir(), "sidecar.db"))
		if err != nil {
			b.Fatal(err)
		}
		if err := db.RefreshIndex(vaultDir); err != nil {
			_ = db.Close()
			b.Fatal(err)
		}
		if _, err := db.Search("deterministic benchmark"); err != nil {
			_ = db.Close()
			b.Fatal(err)
		}
		if err := db.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
