//go:build windows

package diff

import "os"

func terminationSignal(_ *os.ProcessState) string {
	return ""
}
