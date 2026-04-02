@echo off
setlocal enabledelayedexpansion

REM Add Go to PATH
set PATH=%PATH%;C:\Program Files\Go\bin

REM Get current datetime as YYMMDDHHMM using PowerShell (more compatible)
for /f %%i in ('powershell -NoProfile -Command "Get-Date -Format 'yyMMddHHmm'"') do set DATETIME=%%i

REM Version logic:
REM   build.bat 0.5   -> saves 0.5 to .version, builds with 0.5.DATETIME
REM   build.bat       -> reads from .version (or defaults to 0.3)
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

echo Building Claude2Kiro v%VERSION%...

REM Build with version injected
go build -ldflags "-X github.com/sgeraldes/claude2kiro/internal/tui/menu.Version=%VERSION%" -o claude2kiro.exe main.go

IF %ERRORLEVEL% NEQ 0 (
    echo Build failed!
    exit /b 1
)

echo Build successful: claude2kiro.exe v%VERSION%
