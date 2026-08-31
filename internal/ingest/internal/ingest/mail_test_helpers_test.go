package ingest

import (
	"os"
	"testing"
)

// The legacy mail tests use bare strings as fixture values. The shared
// resolver now treats bare values as environment-variable names, so provide
// those fixture variables without changing the production contract.
func TestMain(m *testing.M) {
	fixtures := map[string]string{
		"myplaintextpw": "myplaintextpw",
		"plaintext-pw":  "plaintext-pw",
		"hunter2":       "hunter2",
	}
	previous := make(map[string]*string, len(fixtures))
	for name, value := range fixtures {
		if old, ok := os.LookupEnv(name); ok {
			copy := old
			previous[name] = &copy
		}
		_ = os.Setenv(name, value)
	}

	code := m.Run()
	for name := range fixtures {
		if old, ok := previous[name]; ok {
			_ = os.Setenv(name, *old)
		} else {
			_ = os.Unsetenv(name)
		}
	}
	os.Exit(code)
}
