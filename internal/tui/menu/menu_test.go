package menu

import (
	"runtime"
	"testing"
)

func hasAction(m Model, a MenuAction) bool {
	for _, it := range m.list.Items() {
		if mi, ok := it.(MenuItem); ok && mi.action == a {
			return true
		}
	}
	return false
}

// TestMenuLaunchItems verifies the launch entries appear only when the server is
// running, and that "Launch Claude Desktop" is offered only on Windows (Claude
// Desktop is a Windows Store app).
func TestMenuLaunchItems(t *testing.T) {
	m := New(120, 40)
	m.SetServerRunning(true, "8080")

	if !hasAction(m, ActionLaunchClaude) {
		t.Fatal("Launch Claude Code should be present when the server is running")
	}
	wantDesktop := runtime.GOOS == "windows"
	if got := hasAction(m, ActionLaunchDesktop); got != wantDesktop {
		t.Fatalf("Launch Claude Desktop present=%v, want %v (GOOS=%s)", got, wantDesktop, runtime.GOOS)
	}

	// With the server stopped, neither launch entry should appear.
	m.SetServerRunning(false, "")
	if hasAction(m, ActionLaunchClaude) || hasAction(m, ActionLaunchDesktop) {
		t.Fatal("launch entries should not appear when the server is stopped")
	}
}
