// Package reference binds the deliberately corrected renderer oracle separately
// from the historical, migration-wide source pin. No environment override.
package reference

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func Current() inventory.Oracle {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		panic("renderer reference source unavailable")
	}
	root, err := fixtureoracle.FindRepositoryRoot(filepath.Dir(source))
	if err != nil {
		panic(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "scripts/rust-port/pdfops/reference-v2.json"))
	if err != nil {
		panic(err)
	}
	var ref struct {
		SchemaVersion int              `json:"schema_version"`
		Compiler      string           `json:"compiler"`
		Oracle        inventory.Oracle `json:"oracle"`
	}
	if err := json.Unmarshal(data, &ref); err != nil {
		panic(err)
	}
	if ref.SchemaVersion != 2 || ref.Compiler != "go1.26.10" || runtime.Version() != ref.Compiler {
		panic("renderer requires reference v2 and Go1.26.10")
	}
	if err := fixtureoracle.ValidateSource(root, ref.Oracle); err != nil {
		panic(fmt.Errorf("renderer corrected reference: %w", err))
	}
	return ref.Oracle
}
