# Installation

## Windows

### Quick install with PowerShell

```powershell
irm https://raw.githubusercontent.com/sgeraldes/claude2kiro/main/install.ps1 | iex
```

This installs:

- `%USERPROFILE%\.local\bin\claude2kiro.exe`
- `%USERPROFILE%\.claude2kiro\bin\claude2kiro-X.Y.Z.exe`

If `%USERPROFILE%\.local\bin` is not already in your user `PATH`, the installer adds it automatically.

Then run:

```powershell
claude2kiro login
claude2kiro run
```

### Manual install from GitHub Releases

1. Download the latest Windows assets from:
   - `https://github.com/sgeraldes/claude2kiro/releases`
2. Save:
   - `claude2kiro-launcher-windows-amd64.exe` as `%USERPROFILE%\.local\bin\claude2kiro.exe`
   - `claude2kiro-windows-amd64.exe` as `%USERPROFILE%\.claude2kiro\bin\claude2kiro-X.Y.Z.exe`
3. Create `%USERPROFILE%\.claude2kiro\bin\current.txt` containing the version only, for example:

```text
1.0.0
```

4. Ensure `%USERPROFILE%\.local\bin` is on your user `PATH`

## Linux / macOS

### Quick install

```bash
curl -fsSL https://raw.githubusercontent.com/sgeraldes/claude2kiro/main/install.sh | bash
```

This installs:

- `~/.local/bin/claude2kiro`
- `~/.claude2kiro/bin/claude2kiro-X.Y.Z`

Then run:

```bash
claude2kiro login
claude2kiro run
```

## Build from source

### Windows

```powershell
& 'C:/Program Files/Go/bin/go.exe' build -o claude2kiro.exe main.go
& 'C:/Program Files/Go/bin/go.exe' build -o claude2kiro-launcher.exe ./cmd/launcher
```

### Any platform with Go installed

```bash
go build -o claude2kiro main.go
go build -o claude2kiro-launcher ./cmd/launcher
```

## After installation

### Login

```bash
claude2kiro login
```

### Run Claude Code through Kiro

```bash
claude2kiro run
```

### Optional: configure Claude Code globally

```bash
claude2kiro claude
```
