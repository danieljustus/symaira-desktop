package reference

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func TestCorrectedReferenceRejectsHistoricalProduction(t *testing.T) {
	current := Current()
	_, source, _, _ := runtime.Caller(0)
	root, err := fixtureoracle.FindRepositoryRoot(filepath.Dir(source))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "scripts/rust-port/pdfops/reference-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ref struct {
		Historical string `json:"historical_oracle"`
	}
	if err := json.Unmarshal(data, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.Historical == current.Commit || ref.Historical == "" {
		t.Fatal("missing distinct historical source")
	}
	if err := fixtureoracle.ValidateSource(root, current); err != nil {
		t.Fatal(err)
	}
	if err := fixtureoracle.ValidateSource(root, inventory.Oracle{Commit: ref.Historical, Release: "historical"}); err == nil {
		t.Fatal("current production was accepted under the historical source identity")
	}
}
