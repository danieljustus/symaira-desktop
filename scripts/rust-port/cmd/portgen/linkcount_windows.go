//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func fileLinkCount(path string, _ os.FileInfo) (uint64, error) {
	//nolint:gosec // opening a validated fixture destination for read-only metadata
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return 0, fmt.Errorf("GetFileInformationByHandle: %w", err)
	}
	return uint64(info.NumberOfLinks), nil
}
