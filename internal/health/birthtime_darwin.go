//go:build darwin

package health

import (
	"os"
	"syscall"
	"time"
)

func fileBirthTime(info os.FileInfo) (time.Time, bool) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		sec := stat.Birthtimespec.Sec
		nsec := stat.Birthtimespec.Nsec
		if sec > 0 {
			return time.Unix(sec, nsec).UTC(), true
		}
	}
	return time.Time{}, false
}
