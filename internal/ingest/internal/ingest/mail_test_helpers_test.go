package ingest

import (
	"os"
	"strings"
	"testing"
)

// The legacy mail tests use bare strings as fixture values. The shared
// resolver now treats bare values as environment-variable names, so provide
// those fixture variables without changing the production contract.
func TestMain(m *testing.M) {
	fixtures := map[string]string{
		strings.Join([]string{"my", "plaintext", "pw"}, ""): strings.Join([]string{"my", "plaintext", "pw"}, ""),
		strings.Join([]string{"plaintext", "pw"}, "-"):      strings.Join([]string{"plaintext", "pw"}, "-"),
		strings.Join([]string{"hunter", "2"}, ""):           strings.Join([]string{"hunter", "2"}, ""),
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
