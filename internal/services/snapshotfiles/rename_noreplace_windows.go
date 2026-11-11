//go:build windows

package snapshotfiles

import (
	"golang.org/x/sys/windows"
	"os"
	"runtime"
	"unsafe"
)

// This layout follows x/sys's native file rename integration test. Zero flags
// prohibit replacing an existing destination. The two directory handles are
// captured originals; names never reconstruct an absolute path after a rename.
type snapshotFileRenameInformation struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

func snapshotMoveNoReplace(from *os.File, old string, to *os.File, next string) error {
	defer runtime.KeepAlive(from)
	defer runtime.KeepAlive(to)
	name, err := windows.NewNTUnicodeString(old)
	if err != nil {
		return snapshotWindowsMoveError(err)
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(from.Fd()), ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.DELETE|windows.SYNCHRONIZE, &attributes, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		return snapshotWindowsMoveError(err)
	}
	defer windows.CloseHandle(handle)
	target, err := windows.UTF16FromString(next)
	if err != nil {
		return err
	}
	var layout snapshotFileRenameInformation
	nameBytes := (len(target) - 1) * 2
	size := int(unsafe.Offsetof(layout.FileName)) + nameBytes
	buffer := make([]byte, size)
	info := (*snapshotFileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.RootDirectory = windows.Handle(to.Fd())
	info.FileNameLength = uint32(nameBytes)
	copy(unsafe.Slice(&info.FileName[0], len(target)-1), target[:len(target)-1])
	err = windows.NtSetInformationFile(handle, &status, &buffer[0], uint32(size), windows.FileRenameInformation)
	runtime.KeepAlive(from)
	runtime.KeepAlive(to)
	return snapshotWindowsMoveError(err)
}

func snapshotWindowsMoveError(err error) error {
	if status, ok := err.(windows.NTStatus); ok {
		return status.Errno()
	}
	return err
}
