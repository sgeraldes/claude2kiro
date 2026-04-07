# Winget Publishing

`claude2kiro` is a good fit for **winget** on Windows.

## Recommended package type

Use a **portable** winget package that installs only the launcher:

- Asset: `claude2kiro-launcher-windows-amd64.exe`
- Install location in user PATH: `%USERPROFILE%\.local\bin\claude2kiro.exe`

This works because the launcher already knows how to:

- create `%USERPROFILE%\.claude2kiro\bin\`
- download the latest real app binary on first run
- keep `current.txt` updated
- self-update in the background

That means winget only needs to distribute the launcher.

## Current blockers

To publish to winget, the project must have a **public GitHub Release** with a stable downloadable asset URL.

Required release asset:

- `claude2kiro-launcher-windows-amd64.exe`

At publish time you also need:

- exact version, e.g. `1.0.0`
- SHA256 of the downloadable launcher asset
- public release URL
- package metadata (publisher, homepage, license)

## Suggested package metadata

- **PackageIdentifier**: `sgeraldes.claude2kiro`
- **PackageName**: `claude2kiro`
- **Publisher**: `Sebastian Geraldes`
- **ShortDescription**: `Use Claude Code with Kiro authentication`
- **License**: `MIT`
- **Homepage**: `https://github.com/sgeraldes/claude2kiro`
- **Tags**: `claude`, `kiro`, `anthropic`, `cli`, `proxy`, `ai`

## Publish flow

### Option A: Submit manually to winget-pkgs

1. Create a public GitHub Release
2. Download the launcher asset and compute SHA256:

```powershell
Invoke-WebRequest https://github.com/sgeraldes/claude2kiro/releases/download/v1.0.0/claude2kiro-launcher-windows-amd64.exe -OutFile claude2kiro-launcher-windows-amd64.exe
Get-FileHash .\claude2kiro-launcher-windows-amd64.exe -Algorithm SHA256
```

3. Generate the manifest with `wingetcreate`
4. Submit the PR to `microsoft/winget-pkgs`

### Option B: Use wingetcreate

Install:

```powershell
winget install wingetcreate
```

Then create/update the manifest:

```powershell
wingetcreate new \
  --id sgeraldes.claude2kiro \
  --version 1.0.0 \
  --installer-url https://github.com/sgeraldes/claude2kiro/releases/download/v1.0.0/claude2kiro-launcher-windows-amd64.exe
```

Or update an existing package:

```powershell
wingetcreate update sgeraldes.claude2kiro \
  --version 1.0.0 \
  --urls https://github.com/sgeraldes/claude2kiro/releases/download/v1.0.0/claude2kiro-launcher-windows-amd64.exe
```

## Example manifest shape

Portable installer manifests usually look like this conceptually:

- `defaultLocale`
- `installer`
- `version`

Key installer settings:

- `InstallerType: portable`
- `Commands:`
  - `claude2kiro`
- `InstallerUrl:` release asset URL
- `InstallerSha256:` computed from the release asset

## Recommendation

Before submitting to winget, make sure:

1. The GitHub repo is public
2. There is at least one public Release
3. The release contains `claude2kiro-launcher-windows-amd64.exe`
4. The README includes the Windows installer and winget install examples

## Future README addition

Once the package is approved, add:

```powershell
winget install sgeraldes.claude2kiro
```

and later:

```powershell
winget upgrade sgeraldes.claude2kiro
```
