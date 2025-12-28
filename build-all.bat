@echo off
setlocal enabledelayedexpansion

REM Add Go to PATH
set PATH=%PATH%;C:\Program Files\Go\bin

REM Get current datetime as YYMMDDHHMM using PowerShell
for /f %%i in ('powershell -NoProfile -Command "Get-Date -Format 'yyMMddHHmm'"') do set DATETIME=%%i

REM Set version string
set VERSION=0.2.%DATETIME%
set LDFLAGS=-ldflags "-X github.com/bestk/kiro2cc/internal/tui/menu.Version=%VERSION%"

echo Building kiro2cc v%VERSION% for all platforms...
echo.

REM Create dist directory
if not exist dist mkdir dist

REM Windows AMD64
echo Building Windows AMD64...
set GOOS=windows
set GOARCH=amd64
go build %LDFLAGS% -o dist\kiro2cc-windows-amd64.exe main.go
if %ERRORLEVEL% NEQ 0 goto :error

REM Linux AMD64
echo Building Linux AMD64...
set GOOS=linux
set GOARCH=amd64
go build %LDFLAGS% -o dist\kiro2cc-linux-amd64 main.go
if %ERRORLEVEL% NEQ 0 goto :error

REM Linux ARM64
echo Building Linux ARM64...
set GOOS=linux
set GOARCH=arm64
go build %LDFLAGS% -o dist\kiro2cc-linux-arm64 main.go
if %ERRORLEVEL% NEQ 0 goto :error

REM macOS AMD64 (Intel)
echo Building macOS AMD64...
set GOOS=darwin
set GOARCH=amd64
go build %LDFLAGS% -o dist\kiro2cc-darwin-amd64 main.go
if %ERRORLEVEL% NEQ 0 goto :error

REM macOS ARM64 (Apple Silicon)
echo Building macOS ARM64...
set GOOS=darwin
set GOARCH=arm64
go build %LDFLAGS% -o dist\kiro2cc-darwin-arm64 main.go
if %ERRORLEVEL% NEQ 0 goto :error

echo.
echo Build successful! Binaries in dist\
dir dist
goto :end

:error
echo Build failed!
exit /b 1

:end
