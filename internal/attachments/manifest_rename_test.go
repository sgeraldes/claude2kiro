package attachments

import (
	"errors"
	"os"
	"testing"
	"time"
)

// withRename replaces the manifest rename for one test.
func withRename(t *testing.T, fn func(tmp, path string) error) {
	t.Helper()
	prevRename, prevPause := renameManifest, renameRetryPause
	renameManifest, renameRetryPause = fn, time.Millisecond
	t.Cleanup(func() { renameManifest, renameRetryPause = prevRename, prevPause })
}

// A rename that fails while another process briefly holds the manifest
// (Windows: Access is denied) is retried, and the save succeeds.
func TestSaveManifestRetriesARenameThatIsBrieflyDenied(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	withRename(t, func(tmp, path string) error {
		calls++
		if calls <= 2 {
			return &os.LinkError{Op: "rename", Old: tmp, New: path, Err: os.ErrPermission}
		}
		return os.Rename(tmp, path)
	})

	if _, _, err := store.Save([]byte("image bytes"), "image/png"); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if calls != 3 {
		t.Fatalf("rename calls = %d, want 3 (two denied, then success)", calls)
	}
}

// Any other rename error fails the save at once.
func TestSaveManifestDoesNotRetryOtherRenameErrors(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	withRename(t, func(tmp, path string) error {
		calls++
		return errors.New("disk on fire")
	})

	if err := store.SaveManifest(); err == nil {
		t.Fatal("a failed rename must fail the save")
	}
	if calls != 1 {
		t.Fatalf("rename calls = %d, want 1", calls)
	}
}

// A rename that stays denied gives up after a few attempts.
func TestSaveManifestGivesUpOnARenameThatStaysDenied(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	withRename(t, func(tmp, path string) error {
		calls++
		return &os.LinkError{Op: "rename", Old: tmp, New: path, Err: os.ErrPermission}
	})

	if err := store.SaveManifest(); err == nil {
		t.Fatal("a rename denied every time must fail the save")
	}
	if calls != renameAttempts {
		t.Fatalf("rename calls = %d, want %d", calls, renameAttempts)
	}
}
