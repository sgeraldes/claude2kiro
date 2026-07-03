param(
    [string]$Binary = (Join-Path (Get-Location) "claude2kiro.exe"),
    [string]$Model = "claude-sonnet-4-6",
    [string]$OutDir = (Join-Path (Get-Location) ("logs\credit-benchmark-" + (Get-Date -Format "yyyyMMdd-HHmmss"))),
    [string[]]$Modes = @("full", "current-only", "recent-compact", "none-text", "aggressive-cache"),
    [ValidateSet("no-tool", "tool")]
    [string]$Scenario = "no-tool"
)

$ErrorActionPreference = "Stop"

function Get-FreePort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Parse("127.0.0.1"), 0)
    $listener.Start()
    $port = $listener.LocalEndpoint.Port
    $listener.Stop()
    return $port
}

function Wait-Port {
    param([int]$Port)
    for ($i = 0; $i -lt 80; $i++) {
        try {
            $client = [System.Net.Sockets.TcpClient]::new()
            $iar = $client.BeginConnect("127.0.0.1", $Port, $null, $null)
            if ($iar.AsyncWaitHandle.WaitOne(500)) {
                $client.EndConnect($iar)
                $client.Close()
                return
            }
            $client.Close()
        } catch {
        }
        Start-Sleep -Milliseconds 250
    }
    throw "Proxy on port $Port did not become reachable"
}

function Set-AdvancedValue {
    param(
        [string]$Text,
        [string]$Key,
        [string]$Value
    )
    $pattern = "(?m)^    $([regex]::Escape($Key)):.*$"
    if ($Text -match $pattern) {
        return [regex]::Replace($Text, $pattern, "    ${Key}: $Value")
    }
    if ($Text -match "(?m)^    stable_conversation_id:.*$") {
        return [regex]::Replace($Text, "(?m)^(    stable_conversation_id:.*)$", "`$1`n    ${Key}: $Value", 1)
    }
    return $Text
}

function Set-LoggingDirectory {
    param(
        [string]$Text,
        [string]$Directory
    )
    $yamlPath = $Directory.Replace("\", "/")
    return [regex]::Replace($Text, "(?m)^    directory:.*$", "    directory: $yamlPath", 1)
}

function Apply-ModeConfig {
    param(
        [string]$BaseConfig,
        [string]$Mode,
        [string]$LogDir
    )

    $text = Set-LoggingDirectory $BaseConfig $LogDir
    $text = Set-AdvancedValue $text "stable_conversation_id" "true"
    $text = Set-AdvancedValue $text "comparison_mode" "false"
    $text = Set-AdvancedValue $text "anthropic_direct" "false"
    $text = Set-AdvancedValue $text "debug_mode" "false"
    $text = Set-AdvancedValue $text "history_recent_turns" "4"
    $text = Set-AdvancedValue $text "tool_compact_max_chars" "1024"

    switch ($Mode) {
        "full" {
            $text = Set-AdvancedValue $text "history_mode" "full"
            $text = Set-AdvancedValue $text "tool_mode" "full"
            $text = Set-AdvancedValue $text "aggressive_cache_points" "false"
        }
        "current-only" {
            $text = Set-AdvancedValue $text "history_mode" "current_only"
            $text = Set-AdvancedValue $text "tool_mode" "full"
            $text = Set-AdvancedValue $text "aggressive_cache_points" "false"
        }
        "recent-compact" {
            $text = Set-AdvancedValue $text "history_mode" "recent"
            $text = Set-AdvancedValue $text "history_recent_turns" "2"
            $text = Set-AdvancedValue $text "tool_mode" "compact"
            $text = Set-AdvancedValue $text "tool_compact_max_chars" "512"
            $text = Set-AdvancedValue $text "aggressive_cache_points" "false"
        }
        "none-text" {
            $text = Set-AdvancedValue $text "history_mode" "full"
            $text = Set-AdvancedValue $text "tool_mode" "none_text"
            $text = Set-AdvancedValue $text "aggressive_cache_points" "false"
        }
        "aggressive-cache" {
            $text = Set-AdvancedValue $text "history_mode" "full"
            $text = Set-AdvancedValue $text "tool_mode" "full"
            $text = Set-AdvancedValue $text "aggressive_cache_points" "true"
        }
        default {
            throw "Unknown benchmark mode: $Mode"
        }
    }
    return $text
}

function Parse-BenchmarkLog {
    param([string]$LogDir)
    $logFiles = Get-ChildItem -Path $LogDir -Filter "*.log" -ErrorAction SilentlyContinue
    $text = ""
    foreach ($file in $logFiles) {
        $text += "`n" + (Get-Content -Raw -Path $file.FullName)
    }

    $requestMatches = [regex]::Matches($text, "Request convId=.*")
    $meterMatches = [regex]::Matches($text, "Kiro metering: ([0-9.]+)")
    $bytes = @()
    $historyLens = @()
    $toolCounts = @()
    foreach ($match in $requestMatches) {
        $line = $match.Value
        $b = [regex]::Match($line, "reqBytes=(\d+)")
        if ($b.Success) { $bytes += [int]$b.Groups[1].Value }
        $h = [regex]::Match($line, "historyLen=(\d+)")
        if ($h.Success) { $historyLens += [int]$h.Groups[1].Value }
        $t = [regex]::Match($line, "tools=(\d+)")
        if ($t.Success) { $toolCounts += [int]$t.Groups[1].Value }
    }

    $credits = 0.0
    foreach ($match in $meterMatches) {
        $credits += [double]$match.Groups[1].Value
    }

    return [pscustomobject]@{
        Requests = $requestMatches.Count
        MeteringEvents = $meterMatches.Count
        TotalCredits = [math]::Round($credits, 6)
        RequestBytes = $bytes
        TotalRequestBytes = ($bytes | Measure-Object -Sum).Sum
        HistoryLens = $historyLens
        ToolCounts = $toolCounts
    }
}

function Parse-ClaudeOutput {
    param([object[]]$Output)

    $text = ($Output | ForEach-Object { $_.ToString() }) -join "`n"
    $jsonStart = $text.IndexOf("[")
    if ($jsonStart -lt 0) {
        $jsonStart = $text.IndexOf("{")
    }
    if ($jsonStart -lt 0) {
        throw "Claude output did not contain JSON"
    }
    $jsonText = $text.Substring($jsonStart)
    $events = $jsonText | ConvertFrom-Json
    $result = @($events | Where-Object { $_.type -eq "result" } | Select-Object -Last 1)
    if (-not $result) {
        throw "Claude output did not contain a result event"
    }
    return $result[0]
}

if (-not (Test-Path $Binary)) {
    throw "Binary not found: $Binary"
}
if (-not (Get-Command claude -ErrorAction SilentlyContinue)) {
    throw "Claude Code CLI not found on PATH"
}

$configPath = Join-Path $HOME ".claude2kiro\config.yaml"
if (-not (Test-Path $configPath)) {
    throw "Config not found: $configPath"
}

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$backupPath = Join-Path $OutDir "config.backup.yaml"
Copy-Item -LiteralPath $configPath -Destination $backupPath -Force
$baseConfig = Get-Content -Raw -LiteralPath $configPath

$envKeys = @(
    "ANTHROPIC_BASE_URL",
    "ANTHROPIC_AUTH_TOKEN",
    "ANTHROPIC_API_KEY",
    "CLAUDE_CODE_USE_BEDROCK",
    "CLAUDE_CODE_USE_VERTEX",
    "CLOUD_ML_REGION",
    "AWS_REGION",
    "AWS_PROFILE",
    "CLAUDE2KIRO"
)
$oldEnv = @{}
foreach ($key in $envKeys) {
    $oldEnv[$key] = [Environment]::GetEnvironmentVariable($key, "Process")
}

$prompts = @(
    [pscustomobject]@{
        Text = 'Reply with exactly: Hi'
        Expected = '^Hi$'
    },
    [pscustomobject]@{
        Text = 'In one sentence, say what a local API proxy does. Do not use tools.'
        Expected = '(?i)local API proxy|API proxy|proxy'
    },
    [pscustomobject]@{
        Text = 'Reply with exactly: Done'
        Expected = '^Done$'
    }
)

$summary = @()

try {
    if ($Scenario -eq "tool" -and -not $PSBoundParameters.ContainsKey("Modes")) {
        $Modes = @("full", "current-only", "recent-compact", "aggressive-cache")
    }

    if ($Scenario -eq "tool") {
        $prompts = @(
            [pscustomobject]@{
                Text = 'Use the PowerShell tool to run: Write-Output "TOOL_OK_1". Then reply with exactly: TOOL_OK_1'
                Expected = '^TOOL_OK_1$'
            },
            [pscustomobject]@{
                Text = 'Use the PowerShell tool to run: Write-Output "TOOL_OK_2". Then reply with exactly: TOOL_OK_2'
                Expected = '^TOOL_OK_2$'
            }
        )
    }

    foreach ($mode in $Modes) {
        $modeDir = Join-Path $OutDir $mode
        $logDir = Join-Path $modeDir "proxy-logs"
        New-Item -ItemType Directory -Force -Path $logDir | Out-Null

        $modeConfig = Apply-ModeConfig $baseConfig $mode $logDir
        Set-Content -LiteralPath $configPath -Value $modeConfig -NoNewline

        $port = Get-FreePort
        $serverOut = Join-Path $modeDir "server.out.log"
        $serverErr = Join-Path $modeDir "server.err.log"
        $proc = Start-Process -FilePath $Binary -ArgumentList @("server", "$port") -WindowStyle Hidden -PassThru -RedirectStandardOutput $serverOut -RedirectStandardError $serverErr

        try {
            Wait-Port $port
            [Environment]::SetEnvironmentVariable("ANTHROPIC_BASE_URL", "http://127.0.0.1:$port", "Process")
            [Environment]::SetEnvironmentVariable("ANTHROPIC_AUTH_TOKEN", "claude2kiro", "Process")
            [Environment]::SetEnvironmentVariable("CLAUDE2KIRO", "1", "Process")
            foreach ($key in @("ANTHROPIC_API_KEY", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLOUD_ML_REGION", "AWS_REGION", "AWS_PROFILE")) {
                [Environment]::SetEnvironmentVariable($key, $null, "Process")
            }

            $session = [guid]::NewGuid().ToString()
            $turn = 0
            $turnResults = @()
            foreach ($promptSpec in $prompts) {
                $turn++
                $outPath = Join-Path $modeDir ("turn-$turn.json")
                if ($turn -eq 1) {
                    $args = @("--print", "--output-format", "json", "--model", $Model, "--permission-mode", "bypassPermissions", "--session-id", $session, $promptSpec.Text)
                } else {
                    $args = @("--print", "--output-format", "json", "--model", $Model, "--permission-mode", "bypassPermissions", "--resume", $session, $promptSpec.Text)
                }
                $prevErrorActionPreference = $ErrorActionPreference
                $ErrorActionPreference = "Continue"
                $output = & claude @args 2>&1
                $exitCode = $LASTEXITCODE
                $ErrorActionPreference = $prevErrorActionPreference
                if ($exitCode -ne 0) {
                    throw "Claude exited with code $exitCode in mode $mode turn $turn"
                }
                $output | Set-Content -LiteralPath $outPath
                $parsed = Parse-ClaudeOutput $output
                if ($parsed.session_id) {
                    $session = $parsed.session_id
                }
                $resultText = [string]$parsed.result
                $turnResults += $resultText
                if ($resultText -notmatch $promptSpec.Expected) {
                    throw "Unexpected Claude result in mode $mode turn $turn`: $resultText"
                }
            }
        } finally {
            if ($proc -and -not $proc.HasExited) {
                Stop-Process -Id $proc.Id -Force
                $proc.WaitForExit()
            }
        }

        Start-Sleep -Milliseconds 500
        $metrics = Parse-BenchmarkLog $logDir
        $summary += [pscustomobject]@{
            Mode = $mode
            Requests = $metrics.Requests
            MeteringEvents = $metrics.MeteringEvents
            TotalCredits = $metrics.TotalCredits
            TotalRequestBytes = $metrics.TotalRequestBytes
            RequestBytes = $metrics.RequestBytes
            HistoryLens = $metrics.HistoryLens
            ToolCounts = $metrics.ToolCounts
            Results = $turnResults
            Directory = $modeDir
        }
    }
} finally {
    Copy-Item -LiteralPath $backupPath -Destination $configPath -Force
    foreach ($key in $envKeys) {
        [Environment]::SetEnvironmentVariable($key, $oldEnv[$key], "Process")
    }
}

$summaryPath = Join-Path $OutDir "summary.json"
$summary | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $summaryPath
$summary | Format-Table -AutoSize
Write-Host "Summary: $summaryPath"
