# Shared helpers for the native environment scripts (Windows PowerShell 5.1 compatible).
# Dot-source from install/up/down/status: . "$PSScriptRoot\_common.ps1"

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$script:RepoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$script:ToolsDir = Join-Path $RepoRoot 'tools'
$script:BinDir = Join-Path $ToolsDir 'bin'
$script:DataDir = Join-Path $ToolsDir 'data'
$script:RunDir = Join-Path $ToolsDir 'run'
$script:DownloadDir = Join-Path $ToolsDir 'downloads'
$script:ClamDir = Join-Path $ToolsDir 'clamav'
$script:VersionsFile = Join-Path $PSScriptRoot 'versions.json'

function Write-Step([string]$Message) { Write-Host "==> $Message" -ForegroundColor Cyan }
function Write-Ok([string]$Message) { Write-Host "    ok  $Message" -ForegroundColor Green }
function Write-Warn([string]$Message) { Write-Host "    !!  $Message" -ForegroundColor Yellow }
function Write-Fail([string]$Message) { Write-Host "    XX  $Message" -ForegroundColor Red }

function Import-DotEnv {
    # Loads KEY=VALUE lines from .env into the process environment; never prints values.
    $envFile = Join-Path $RepoRoot '.env'
    if (-not (Test-Path $envFile)) {
        Write-Warn ".env not found; copy .env.example to .env first"
        return
    }
    Get-Content $envFile | ForEach-Object {
        if ($_ -match '^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*$') {
            [Environment]::SetEnvironmentVariable($matches[1], $matches[2])
        }
    }
}

function Get-EnvOrDefault([string]$Name, [string]$Default) {
    $v = [Environment]::GetEnvironmentVariable($Name)
    if ([string]::IsNullOrEmpty($v)) { return $Default }
    return $v
}

function Get-VersionManifest {
    return Get-Content $VersionsFile -Raw | ConvertFrom-Json
}

function Initialize-Directory([string]$Path) {
    if (-not (Test-Path $Path)) { New-Item -ItemType Directory -Path $Path -Force | Out-Null }
}

function Get-Sha256([string]$Path) {
    return (Get-FileHash -Algorithm SHA256 -Path $Path).Hash.ToLowerInvariant()
}

function Get-PidFile([string]$Name) { return Join-Path $RunDir "$Name.pid" }

function Get-RunningProcess([string]$Name) {
    # Returns the process recorded in tools/run/<name>.pid if it is still alive, else $null.
    $pidFile = Get-PidFile $Name
    if (-not (Test-Path $pidFile)) { return $null }
    $procId = (Get-Content $pidFile -Raw).Trim()
    if (-not ($procId -match '^\d+$')) { return $null }
    $p = Get-Process -Id ([int]$procId) -ErrorAction SilentlyContinue
    if ($null -eq $p) { return $null }
    return $p
}

function Test-TcpPort([string]$HostName, [int]$Port, [int]$TimeoutMs = 1500) {
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $async = $client.BeginConnect($HostName, $Port, $null, $null)
        if (-not $async.AsyncWaitHandle.WaitOne($TimeoutMs, $false)) { return $false }
        $client.EndConnect($async)
        return $true
    } catch {
        return $false
    } finally {
        $client.Close()
    }
}

function Test-Http([string]$Url, [int]$TimeoutSec = 3) {
    try {
        $r = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec $TimeoutSec
        return ($r.StatusCode -ge 200 -and $r.StatusCode -lt 400)
    } catch {
        return $false
    }
}

function Test-ClamdPing([string]$HostName, [int]$Port) {
    # clamd answers "PONG" to the PING command over its TCP socket.
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $client.Connect($HostName, $Port)
        $stream = $client.GetStream()
        $bytes = [Text.Encoding]::ASCII.GetBytes("zPING`0")
        $stream.Write($bytes, 0, $bytes.Length)
        $stream.ReadTimeout = 3000
        $buffer = New-Object byte[] 16
        $n = $stream.Read($buffer, 0, $buffer.Length)
        return ([Text.Encoding]::ASCII.GetString($buffer, 0, $n)).Contains('PONG')
    } catch {
        return $false
    } finally {
        $client.Close()
    }
}

function Wait-Until([scriptblock]$Check, [string]$What, [int]$TimeoutSec = 60) {
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        if (& $Check) { Write-Ok "$What is up"; return $true }
        Start-Sleep -Milliseconds 800
    }
    Write-Fail "$What did not become healthy within ${TimeoutSec}s (see tools\run\*.log)"
    return $false
}

function Invoke-ServiceStart([string]$Name, [string]$Exe, [string[]]$Arguments, [hashtable]$Environment = @{}) {
    # Starts a hidden process, records its pid and redirects output to tools/run/<name>.log.
    Initialize-Directory $RunDir
    $existing = Get-RunningProcess $Name
    if ($null -ne $existing) { Write-Ok "$Name already running (pid $($existing.Id))"; return $existing }
    foreach ($k in $Environment.Keys) { [Environment]::SetEnvironmentVariable($k, $Environment[$k]) }
    $log = Join-Path $RunDir "$Name.log"
    $err = Join-Path $RunDir "$Name.err.log"
    # Start-Process joins the list with spaces; paths with spaces (this repository has one)
    # must be quoted or the child sees them as several arguments.
    $quoted = $Arguments | ForEach-Object { if ($_ -match '\s') { '"' + $_ + '"' } else { $_ } }
    $p = Start-Process -FilePath $Exe -ArgumentList $quoted -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput $log -RedirectStandardError $err -WorkingDirectory $ToolsDir
    Set-Content -Path (Get-PidFile $Name) -Value $p.Id -Encoding ascii
    Write-Ok "$Name started (pid $($p.Id), log tools\run\$Name.log)"
    return $p
}

function Invoke-ServiceStop([string]$Name) {
    $p = Get-RunningProcess $Name
    if ($null -eq $p) {
        Write-Ok "$Name not running"
    } else {
        Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
        Write-Ok "$Name stopped (pid $($p.Id))"
    }
    $pidFile = Get-PidFile $Name
    if (Test-Path $pidFile) { Remove-Item $pidFile -Force -ErrorAction SilentlyContinue }
}
