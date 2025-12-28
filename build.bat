@echo off
setlocal enabledelayedexpansion

REM Add Go to PATH
set PATH=%PATH%;C:\Program Files\Go\bin

REM Get current datetime as YYMMDDHHMM using PowerShell (more compatible)
for /f %%i in ('powershell -NoProfile -Command "Get-Date -Format 'yyMMddHHmm'"') do set DATETIME=%%i

REM Set version string (major.minor.datetime)
set VERSION=0.2.%DATETIME%

echo Building kiro2cc v%VERSION%...

REM Build with version injected
go build -ldflags "-X github.com/bestk/kiro2cc/internal/tui/menu.Version=%VERSION%" -o kiro2cc.exe main.go

IF %ERRORLEVEL% NEQ 0 (
    echo Build failed!
    exit /b 1
)

echo Build successful: kiro2cc.exe v%VERSION%
