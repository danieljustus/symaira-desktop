package doctor

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRunReportsStableChecksWithRemediation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	r, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Failed {
		t.Fatal("empty directory must fail room checks")
	}
	if len(r.Checks) < 6 {
		t.Fatalf("got %d checks", len(r.Checks))
	}
	for _, c := range r.Checks {
		if c.Name == "" || c.Status == "" || c.Remediation == "" {
			t.Errorf("incomplete check: %+v", c)
		}
	}
	if _, err := json.Marshal(r); err != nil {
		t.Fatal(err)
	}
}
