//go:build windows

package diff

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Go 1.26 Root.Symlink cannot classify directory targets through absolute
// aliases. Retain Root's parent capability and create only its basename;
// neither link creation nor failure cleanup uses an absolute destination path.
func createSandboxSymlink(root *os.Root, target, path string) (result error) {
	resolved, err := resolveSandboxSymlinkTarget(root, path, target)
	if err != nil {
		return err
	}
	info, err := root.Stat(resolved)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	options := uint32(windows.FILE_NON_DIRECTORY_FILE)
	if err == nil && info.IsDir() {
		options = windows.FILE_DIRECTORY_FILE
	}
	buffer, err := sandboxSymlinkReparseData(target)
	if err != nil {
		return err
	}
	name := filepath.Base(path)
	if !filepath.IsLocal(name) || name == "." || name == ".." {
		return fmt.Errorf("symlink destination must have a local basename: %q", path)
	}
	parent, err := root.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, parent.Close()) }()
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(parent.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	err = withSandboxSymlinkPrivilege(func() error {
		var handle windows.Handle
		err := windows.NtCreateFile(&handle, windows.SYNCHRONIZE|windows.FILE_WRITE_ATTRIBUTES|windows.DELETE,
			&attributes, &windows.IO_STATUS_BLOCK{}, nil, windows.FILE_ATTRIBUTE_NORMAL, 0, windows.FILE_CREATE,
			options|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(handle)
		var returned uint32
		err = windows.DeviceIoControl(handle, windows.FSCTL_SET_REPARSE_POINT, &buffer[0], uint32(len(buffer)), nil, 0, &returned, nil)
		if err != nil {
			// Delete only the object FILE_CREATE created, even if its name moves.
			deleteOwned := byte(1)
			cleanupErr := windows.NtSetInformationFile(handle, &windows.IO_STATUS_BLOCK{}, &deleteOwned, 1, windows.FileDispositionInformation)
			return errors.Join(err, cleanupErr)
		}
		return nil
	})
	if err != nil {
		return &os.LinkError{Op: "symlinkat", Old: target, New: path, Err: err}
	}
	return nil
}

func sandboxSymlinkReparseData(target string) ([]byte, error) {
	substitute := filepath.FromSlash(target)
	var flags uint32 = 1 // SYMLINK_FLAG_RELATIVE
	if filepath.IsAbs(substitute) {
		flags = 0
		switch {
		case strings.HasPrefix(substitute, `\\?\`):
			substitute = `\??\` + substitute[4:]
		case strings.HasPrefix(substitute, `\\`):
			substitute = `\??\UNC\` + substitute[2:]
		default:
			substitute = `\??\` + substitute
		}
	}
	sub, err := windows.UTF16FromString(substitute)
	if err != nil {
		return nil, err
	}
	printName, err := windows.UTF16FromString(target)
	if err != nil {
		return nil, err
	}
	sub, printName = sub[:len(sub)-1], printName[:len(printName)-1]
	if 20+2*(len(sub)+len(printName)) > windows.MAXIMUM_REPARSE_DATA_BUFFER_SIZE {
		return nil, errors.New("symlink target exceeds the Windows reparse buffer limit")
	}
	buffer := make([]byte, 20+2*(len(sub)+len(printName)))
	binary.LittleEndian.PutUint32(buffer, windows.IO_REPARSE_TAG_SYMLINK)
	binary.LittleEndian.PutUint16(buffer[4:], uint16(len(buffer)-8))
	binary.LittleEndian.PutUint16(buffer[10:], uint16(2*len(sub)))
	binary.LittleEndian.PutUint16(buffer[12:], uint16(2*len(sub)))
	binary.LittleEndian.PutUint16(buffer[14:], uint16(2*len(printName)))
	binary.LittleEndian.PutUint32(buffer[16:], flags)
	for index, value := range append(sub, printName...) {
		binary.LittleEndian.PutUint16(buffer[20+2*index:], value)
	}
	return buffer, nil
}

// Match the standard library's temporary symlink privilege, not the process
// token. Failure to acquire it still lets Windows enforce Developer Mode.
func withSandboxSymlinkPrivilege(operation func() error) (result error) {
	runtime.LockOSThread()
	unlock := true
	defer func() {
		if unlock {
			runtime.UnlockOSThread()
		}
	}()
	if err := windows.ImpersonateSelf(windows.SecurityImpersonation); err != nil {
		return operation()
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			// Do not return an impersonating thread to Go's scheduler.
			unlock = false
			result = errors.Join(result, fmt.Errorf("restore symlink thread identity: %w", err))
		}
	}()
	thread, err := windows.GetCurrentThread()
	if err != nil {
		return operation()
	}
	var token windows.Token
	if err := windows.OpenThreadToken(thread, windows.TOKEN_QUERY|windows.TOKEN_ADJUST_PRIVILEGES, false, &token); err != nil {
		return operation()
	}
	defer token.Close()
	name, err := windows.UTF16PtrFromString("SeCreateSymbolicLinkPrivilege")
	if err != nil {
		return err
	}
	privileges := windows.Tokenprivileges{PrivilegeCount: 1}
	if err := windows.LookupPrivilegeValue(nil, name, &privileges.Privileges[0].Luid); err == nil {
		privileges.Privileges[0].Attributes = windows.SE_PRIVILEGE_ENABLED
		_ = windows.AdjustTokenPrivileges(token, false, &privileges, 0, nil, nil)
	}
	return operation()
}
