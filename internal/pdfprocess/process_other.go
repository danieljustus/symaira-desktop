//go:build !unix

package pdfprocess

import (
	"errors"
	"os/exec"
)

// Fail closed until native process-tree ownership is implemented and verified.
func prepare(_ *exec.Cmd) error {
	return errors.New("PDF subprocess isolation is unsupported on this platform")
}

func terminate(_ *exec.Cmd) error {
	return errors.New("PDF subprocess isolation is unsupported on this platform")
}
