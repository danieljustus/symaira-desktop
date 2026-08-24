//go:build windows

package health

import (
	"os"
	"syscall"
	"time"
)

func fileBirthTime(info os.FileInfo) (time.Time, bool) {
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return time.Unix(0, d.CreationTime.Nanoseconds()).UTC(), true
	}
	return time.Time{}, false
}
