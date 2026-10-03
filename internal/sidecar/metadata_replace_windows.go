package sidecar

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func replaceMetadataFile(source, target string) error {
	// Concurrent readers/writers can briefly deny replacement on Windows.
	// Keep the old record intact and retry only its specific sharing failures;
	// persistent permissions and every other filesystem error still propagate.
	for attempt := 0; ; attempt++ {
		err := os.Rename(source, target)
		transient := errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
		if err == nil || !transient || attempt == 9 {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
