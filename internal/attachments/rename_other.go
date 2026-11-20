//go:build !windows

package attachments

// isSharingError is false off Windows: a rename there is not blocked by
// another process's open handle.
func isSharingError(error) bool { return false }
