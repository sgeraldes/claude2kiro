package cmd

import (
	"strings"
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/config"
)

// The TUI export line shows the proxy key and the configured port, and needs
// no token: nothing of the access token can reach the screen.
func TestExportEnvCmdShowsProxyKeyNotToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	port := config.Get().Server.Port

	msg, ok := ExportEnvCmd().(StatusMsg)
	if !ok {
		t.Fatalf("ExportEnvCmd returned %T, want StatusMsg", ExportEnvCmd())
	}
	if msg.IsError {
		t.Fatalf("ExportEnvCmd failed without a token: %s", msg.Message)
	}
	if !strings.Contains(msg.Message, "ANTHROPIC_API_KEY=claude2kiro") {
		t.Fatalf("message lacks the proxy key: %s", msg.Message)
	}
	if !strings.Contains(msg.Message, "http://localhost:"+port) {
		t.Fatalf("message lacks the configured port %s: %s", port, msg.Message)
	}
}
