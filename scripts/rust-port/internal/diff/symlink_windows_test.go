//go:build windows

package diff

import (
	"encoding/binary"
	"os"
	"path/filepath"
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
		{`vault/marker.md`, `vault\marker.md`, 1},
		{`C:\sandbox\directory`, `\??\C:\sandbox\directory`, 0},
		{`C:/sandbox/directory`, `\??\C:\sandbox\directory`, 0},
		{`\\server\share\directory`, `\??\UNC\server\share\directory`, 0},
		{`//server/share/directory`, `\??\UNC\server\share\directory`, 0},
		{`\\?\C:\sandbox\directory`, `\??\C:\sandbox\directory`, 0},
		{`//?/C:/sandbox/directory`, `\??\C:\sandbox\directory`, 0},
		{`\\?\UNC\server\share\directory`, `\??\UNC\server\share\directory`, 0},
		{`//?/UNC/server/share/directory`, `\??\UNC\server\share\directory`, 0},
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

func TestSandboxWindowsExtendedRootSymlinks(t *testing.T) {
	root := t.TempDir()
	if !strings.HasPrefix(root, `\\?\`) {
		if strings.HasPrefix(root, `\\`) {
			root = `\\?\UNC\` + root[2:]
		} else {
			root = `\\?\` + root
		}
	}
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	for _, directory := range []string{home, workspace} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sandbox, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.Close() })
	for _, setup := range []SetupFile{
		{Path: "vault/marker.md", Content: "fixture"},
		{Path: "absolute-directory", Kind: "symlink", LinkTarget: filepath.Join(workspace, "vault")},
		{Path: "absolute-file", Kind: "symlink", LinkTarget: filepath.Join(workspace, "vault", "marker.md")},
		{Path: "slash-directory", Kind: "symlink", LinkTarget: "absolute-directory/../vault"},
		{Path: "slash-file", Kind: "symlink", LinkTarget: "vault/marker.md"},
	} {
		if err := setupSandboxFile(root, home, workspace, setup, nil); err != nil {
			t.Fatalf("create %q under an extended root: %v", setup.Path, err)
		}
		if setup.Kind == "symlink" {
			// Go Readlink exposes the substitute name, not the raw print name.
			got, err := os.Readlink(filepath.Join(workspace, setup.Path))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := resolveSandboxSymlinkTarget(sandbox, filepath.Join("workspace", setup.Path), got); err != nil {
				t.Fatalf("link %q has an invalid observable target %q: %v", setup.Path, got, err)
			}
		}
	}
	if err := validateSandboxSymlink(sandbox, "workspace/outside", root+"-other/target"); err == nil {
		t.Fatal("accepted an extended absolute target outside the sandbox")
	}
	for _, alias := range []string{"absolute-directory/marker.md", "absolute-file", "slash-directory/marker.md", "slash-file"} {
		if content, err := os.ReadFile(filepath.Join(workspace, alias)); err != nil || string(content) != "fixture" {
			t.Fatalf("extended-root alias %q is not traversable: %q, %v", alias, content, err)
		}
	}
	manifest, err := buildManifest(root)
	if err != nil {
		t.Fatalf("extended-root aliases did not retain a valid manifest: %v", err)
	}
	want := map[string]string{
		"workspace/absolute-directory": "<SANDBOX>/workspace/vault",
		"workspace/absolute-file":      "<SANDBOX>/workspace/vault/marker.md",
	}
	for _, entry := range manifest {
		if target, ok := want[entry.Path]; ok {
			if entry.LinkTarget != target {
				t.Fatalf("manifest target for %q = %q, want %q", entry.Path, entry.LinkTarget, target)
			}
			delete(want, entry.Path)
		}
	}
	if len(want) != 0 {
		t.Fatalf("manifest omitted absolute aliases: %v", want)
	}
}
