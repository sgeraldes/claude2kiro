//go:build windows

package attachments

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isSharingError reports a rename that failed because another process had the
// file open in a way that blocks it (an indexer or a virus scanner, briefly).
func isSharingError(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
