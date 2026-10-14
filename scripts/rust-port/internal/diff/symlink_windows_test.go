//go:build windows

package diff

import (
	"encoding/binary"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSandboxWindowsSymlinkReparseData(t *testing.T) {
	for _, test := range []struct {
		target, substitute string
		flags              uint32
	}{
		{`directory`, `directory`, 1},
		{`alias\..\note-ä.md`, `alias\..\note-ä.md`, 1},
		{`C:\sandbox\directory`, `\??\C:\sandbox\directory`, 0},
		{`\\server\share\directory`, `\??\UNC\server\share\directory`, 0},
	} {
		t.Run(test.target, func(t *testing.T) {
			buffer, err := sandboxSymlinkReparseData(test.target)
			if err != nil {
				t.Fatal(err)
			}
			if binary.LittleEndian.Uint32(buffer) != windows.IO_REPARSE_TAG_SYMLINK ||
				int(binary.LittleEndian.Uint16(buffer[4:])) != len(buffer)-8 ||
				binary.LittleEndian.Uint32(buffer[16:]) != test.flags {
				t.Fatal("incorrect symlink reparse header")
			}
			readName := func(offset, size uint16) string {
				units := make([]uint16, int(size)/2)
				for index := range units {
					units[index] = binary.LittleEndian.Uint16(buffer[20+int(offset)+2*index:])
				}
				return windows.UTF16ToString(units)
			}
			if got := readName(binary.LittleEndian.Uint16(buffer[8:]), binary.LittleEndian.Uint16(buffer[10:])); got != test.substitute {
				t.Fatalf("substitute = %q, want %q", got, test.substitute)
			}
			if got := readName(binary.LittleEndian.Uint16(buffer[12:]), binary.LittleEndian.Uint16(buffer[14:])); got != test.target {
				t.Fatalf("print name changed from %q to %q", test.target, got)
			}
		})
	}
	for _, invalid := range []string{"bad\x00target", strings.Repeat("x", windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE)} {
		if buffer, err := sandboxSymlinkReparseData(invalid); err == nil || buffer != nil {
			t.Fatal("accepted an invalid or oversized reparse target")
		}
	}
}
