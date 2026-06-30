@echo off
setlocal enabledelayedexpansion

REM Add Go to PATH
set PATH=%PATH%;C:\Program Files\Go\bin
set CGO_ENABLED=0

REM Get current datetime as YYMMDDHHMM using PowerShell
for /f %%i in ('powershell -NoProfile -Command "Get-Date -Format 'yyMMddHHmm'"') do set DATETIME=%%i

REM Version logic:
REM   build-all.bat 1.4.5 -> saves 1.4.5 to .version, builds with 1.4.5.DATETIME
REM   build-all.bat       -> reads from .version (or defaults to 0.3)
if not "%~1"=="" (
    set BASE=%~1
    echo %~1> .version
) else (
    if exist .version (
        set /p BASE=<.version
    ) else (
        set BASE=0.3
    )
)
set VERSION=%BASE%.%DATETIME%
set LDFLAGS=-ldflags "-X github.com/sgeraldes/claude2kiro/internal/tui/menu.Version=%VERSION%"

echo Building Claude2Kiro v%VERSION% for all platforms...
echo.

REM Create dist directory
if not exist dist mkdir dist

REM Windows AMD64
echo Building Windows AMD64...
set GOOS=windows
set GOARCH=amd64
go build %LDFLAGS% -o dist\claude2kiro-windows-amd64.exe main.go
if %ERRORLEVEL% NEQ 0 goto :error
go build -trimpath -ldflags "-s -w" -o dist\claude2kiro-launcher-windows-amd64.exe .\cmd\launcher
if %ERRORLEVEL% NEQ 0 goto :error

REM Linux AMD64
echo Building Linux AMD64...
set GOOS=linux
set GOARCH=amd64
go build %LDFLAGS% -o dist\claude2kiro-linux-amd64 main.go
if %ERRORLEVEL% NEQ 0 goto :error
go build -trimpath -ldflags "-s -w" -o dist\claude2kiro-launcher-linux-amd64 .\cmd\launcher
if %ERRORLEVEL% NEQ 0 goto :error

REM Linux ARM64
echo Building Linux ARM64...
set GOOS=linux
set GOARCH=arm64
go build %LDFLAGS% -o dist\claude2kiro-linux-arm64 main.go
if %ERRORLEVEL% NEQ 0 goto :error
go build -trimpath -ldflags "-s -w" -o dist\claude2kiro-launcher-linux-arm64 .\cmd\launcher
if %ERRORLEVEL% NEQ 0 goto :error

REM macOS AMD64 (Intel)
echo Building macOS AMD64...
set GOOS=darwin
set GOARCH=amd64
go build %LDFLAGS% -o dist\claude2kiro-darwin-amd64 main.go
if %ERRORLEVEL% NEQ 0 goto :error
go build -trimpath -ldflags "-s -w" -o dist\claude2kiro-launcher-darwin-amd64 .\cmd\launcher
if %ERRORLEVEL% NEQ 0 goto :error

REM macOS ARM64 (Apple Silicon)
echo Building macOS ARM64...
set GOOS=darwin
set GOARCH=arm64
go build %LDFLAGS% -o dist\claude2kiro-darwin-arm64 main.go
if %ERRORLEVEL% NEQ 0 goto :error
go build -trimpath -ldflags "-s -w" -o dist\claude2kiro-launcher-darwin-arm64 .\cmd\launcher
if %ERRORLEVEL% NEQ 0 goto :error

echo.
echo Build successful! Binaries in dist\
dir dist
goto :end

:error
echo Build failed!
exit /b 1

:end
