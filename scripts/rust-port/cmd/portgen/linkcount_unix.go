//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

func fileLinkCount(_ string, info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unsupported stat type %T", info.Sys())
	}
	return uint64(stat.Nlink), nil
}
