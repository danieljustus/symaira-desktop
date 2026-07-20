package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/demo"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

const mockSymmemoryLargeVaultScript = `#!/bin/bash
if [ "$1" = "entity" ] && [ "$2" = "list" ]; then
  echo '[{"id":"e-max","name":"Max Mustermann","type":"person","aliases":[],"description":""},{"id":"e-erika","name":"Erika Mustermann","type":"person","aliases":[],"description":""}]'
elif [ "$1" = "entity" ] && [ "$2" = "neighbors" ]; then
  if [ "$3" = "Max Mustermann" ]; then
    echo '{"nodes":[{"id":"e-erika","name":"Erika Mustermann","type":"person","aliases":[],"description":""}],"edges":[{"from_entity_id":"e-max","to_entity_id":"e-erika","relation_type":"related_to"}]}'
  elif [ "$3" = "Erika Mustermann" ]; then
    echo '{"nodes":[{"id":"e-max","name":"Max Mustermann","type":"person","aliases":[],"description":""}],"edges":[{"from_entity_id":"e-max","to_entity_id":"e-erika","relation_type":"related_to"}]}'
  fi
fi
`

// BenchmarkGraphLargeVaultWithEntities measures Service.Graph over the
// deterministic 10k-document benchmark vault (make benchmark-large) with
// Memory entities present, so the entity-matching pass actually runs against
// every document instead of being skipped. It is intentionally opt-in: like
// BenchmarkLargeVaultIndexAndSearch in internal/demo, the 10k fixture is a
// release-performance measurement, not a unit-test workload.
func BenchmarkGraphLargeVaultWithEntities(b *testing.B) {
	dir := b.TempDir()
	vaultDir := filepath.Join(dir, "vault")
	if err := demo.InitLarge(vaultDir); err != nil {
		b.Fatal(err)
	}

	db, err := sidecar.Open(filepath.Join(dir, "sidecar.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })

	if err := vault.Walk(vaultDir, func(path string) error {
		doc, err := vault.ParseFile(path)
		if err != nil {
			return err
		}
		return db.IndexDocument(doc)
	}); err != nil {
		b.Fatal(err)
	}

	symmemoryPath := filepath.Join(dir, "symmemory")
	if err := os.WriteFile(symmemoryPath, []byte(mockSymmemoryLargeVaultScript), 0755); err != nil { //nolint:gosec // benchmark fixture must be executable
		b.Fatal(err)
	}
	b.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	compose.ResetCache()
	b.Cleanup(compose.ResetCache)

	svc := New(vaultDir, db)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.Graph(); err != nil {
			b.Fatal(err)
		}
	}
}
