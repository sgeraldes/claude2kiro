@echo off
setlocal enabledelayedexpansion

REM Add Go to PATH
set PATH=%PATH%;C:\Program Files\Go\bin

REM Get current datetime as YYMMDDHHMM using PowerShell (more compatible)
for /f %%i in ('powershell -NoProfile -Command "Get-Date -Format 'yyMMddHHmm'"') do set DATETIME=%%i

REM Set version string: build [major.minor] (default: 0.3)
set BASE=0.3
if not "%~1"=="" set BASE=%~1
set VERSION=%BASE%.%DATETIME%

echo Building Claude2Kiro v%VERSION%...

REM Build with version injected
go build -ldflags "-X github.com/sgeraldes/claude2kiro/internal/tui/menu.Version=%VERSION%" -o claude2kiro.exe main.go

IF %ERRORLEVEL% NEQ 0 (
    echo Build failed!
    exit /b 1
)

echo Build successful: claude2kiro.exe v%VERSION%
