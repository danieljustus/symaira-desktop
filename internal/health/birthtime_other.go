//go:build !darwin && !windows

package health

import (
	"os"
	"time"
)

func fileBirthTime(info os.FileInfo) (time.Time, bool) {
	return time.Time{}, false
}
