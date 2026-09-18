//go:build windows

package tokenfile

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// ReadFile reads a token file with delete sharing. Go's os.Open shares
// reads and writes but not deletes, and a writer's rename over the file
// (MoveFileEx with replace) fails while such a handle is open; a reader
// opened this way never blocks a login or a refresh in another process.
func ReadFile(path string) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	return io.ReadAll(f)
}
