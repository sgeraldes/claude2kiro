# Release Plan for Claude2Kiro v0.2.0

## Release Summary

Major release adding an interactive TUI, cross-platform builds, and improved user experience.

## New Features

### Interactive TUI (Bubble Tea)
- Beautiful terminal interface replacing CLI-only interaction
- Real-time dashboard showing server status, credentials, and credits
- Scrollable request log viewer with Tab-based focus switching
- Auto-start server when user has valid token

### Cross-Platform Support
- Windows (amd64)
- Linux (amd64, arm64)
- macOS (Intel amd64, Apple Silicon arm64)

### Launch Scripts
- `claude-kiro.bat` - Windows CMD
- `claude-kiro.ps1` - Windows PowerShell
- `claude-kiro` - Linux/macOS (in ~/.local/bin)

### Improvements
- Scripts placed in PATH-accessible locations
- Better error messages and status display
- Credits progress bar with color coding
- Token expiry countdown

## Bug Fixes
- Fixed response logs showing empty preview
- Fixed menu text mismatch
- Fixed server auto-start (program reference issue)
- Fixed login requiring multiple enter presses

## Files Changed

### New Files
- `internal/tui/` - Complete TUI implementation
  - `tui.go` - Root model and state management
  - `dashboard/` - Server dashboard components
  - `menu/` - Main menu component
  - `login/` - Login flow component
  - `logger/` - Log capture and formatting
- `cmd/commands.go` - Extracted business logic
- `build-all.bat` - Windows cross-platform builder
- `build-all.sh` - Unix cross-platform builder

### Modified Files
- `main.go` - Added TUI routing, login improvements
- `go.mod` - Added Bubble Tea dependencies
- `.gitignore` - Updated for build artifacts
- `README.md` - Updated documentation

## Dependencies Added
```
github.com/charmbracelet/bubbletea v1.2.4
github.com/charmbracelet/bubbles v0.20.0
github.com/charmbracelet/lipgloss v1.0.0
```

## Build Instructions

### Single Platform
```bash
go build -o claude2kiro main.go
```

### All Platforms
```bash
# Windows
build-all.bat

# Linux/macOS
./build-all.sh
```

Outputs to `dist/` directory.

## Release Checklist

- [x] All features implemented and tested
- [x] Cross-platform builds working
- [x] Git commit created
- [ ] Tag release: `git tag v0.2.0`
- [ ] Push to origin: `git push origin main --tags`
- [ ] Create GitHub release with binaries from `dist/`
- [ ] Update release notes on GitHub

## GitHub Release Notes Template

```markdown
## Claude2Kiro v0.2.0

### Highlights
- **Interactive TUI** - Beautiful terminal interface with real-time dashboard
- **Auto-start** - Server starts automatically when you're logged in
- **Cross-platform** - Native binaries for Windows, Linux, and macOS

### New Features
- Bubble Tea TUI with menu navigation and dashboard
- Real-time request logging with scrollable viewer
- Credits display with progress bar
- Token expiry countdown
- `claude-kiro` launch scripts for easy integration

### Bug Fixes
- Fixed response logs showing empty preview
- Fixed server auto-start timing issue
- Fixed login flow requiring multiple enter presses

### Download
| Platform | File |
|----------|------|
| Windows | claude2kiro-windows-amd64.exe |
| Linux (Intel/AMD) | claude2kiro-linux-amd64 |
| Linux (ARM) | claude2kiro-linux-arm64 |
| macOS (Intel) | claude2kiro-darwin-amd64 |
| macOS (Apple Silicon) | claude2kiro-darwin-arm64 |

### Quick Start
```bash
# Run the TUI
./claude2kiro

# Or use commands directly
./claude2kiro login github
./claude2kiro claude  # Creates claude-kiro script
./claude2kiro server
```
```

## Post-Release Tasks

1. Monitor GitHub issues for bug reports
2. Update model mappings if Kiro adds new models
3. Consider adding:
   - Log file export
   - Custom port configuration in TUI
   - Theme customization
