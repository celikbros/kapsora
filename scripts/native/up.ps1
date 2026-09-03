<#
.SYNOPSIS
  Starts the native dependencies for local development: MinIO (+ buckets), ClamAV clamd
  (after freshclam), Mailpit. PostgreSQL is expected to be running as a Windows service.
.NOTES
  Idempotent: services already recorded in tools/run/*.pid and alive are left as they are.
  Credentials come from .env only (KAPSORA_MINIO_ROOT_USER / _PASSWORD).
#>
[CmdletBinding()]
param(
    # Skip the signature update even when the database is missing or older than a day.
    [switch]$SkipFreshclam
)

. "$PSScriptRoot\_common.ps1"
Import-DotEnv

$minioAddr = Get-EnvOrDefault 'KAPSORA_MINIO_ADDR' '127.0.0.1:9000'
$minioConsole = Get-EnvOrDefault 'KAPSORA_MINIO_CONSOLE_ADDR' '127.0.0.1:9001'
$minioUser = Get-EnvOrDefault 'KAPSORA_MINIO_ROOT_USER' 'kapsora'
$minioPass = Get-EnvOrDefault 'KAPSORA_MINIO_ROOT_PASSWORD' 'kapsora_local_minio'
$clamAddr = Get-EnvOrDefault 'KAPSORA_CLAMAV_ADDR' '127.0.0.1:3310'
$mailUi = Get-EnvOrDefault 'KAPSORA_MAILPIT_UI_ADDR' '127.0.0.1:8025'
$smtpAddr = Get-EnvOrDefault 'KAPSORA_SMTP_ADDR' '127.0.0.1:1025'
$buckets = @('quarantine', 'secure', 'exports', 'immutable', 'fiscal')

foreach ($exe in @('minio.exe', 'mc.exe', 'mailpit.exe')) {
    if (-not (Test-Path (Join-Path $BinDir $exe))) { throw "tools\bin\$exe missing; run .\scripts\native\install.ps1 first" }
}
Initialize-Directory $RunDir

Write-Step "PostgreSQL"
$pgHost = '127.0.0.1'; $pgPort = 5432
if ($env:KAPSORA_DATABASE_URL -match '@([^:/]+):(\d+)/') { $pgHost = $matches[1]; $pgPort = [int]$matches[2] }
if (Test-TcpPort $pgHost $pgPort) { Write-Ok "PostgreSQL listening on ${pgHost}:${pgPort}" }
else { Write-Warn "PostgreSQL not reachable on ${pgHost}:${pgPort}; start the postgresql-x64-18 service" }

Write-Step "MinIO"
$minioData = Join-Path $DataDir 'minio'
Initialize-Directory $minioData
$minioPort = [int]($minioAddr.Split(':')[-1])
if ((Test-TcpPort '127.0.0.1' $minioPort) -and ($null -eq (Get-RunningProcess 'minio'))) {
    Write-Warn "something else already listens on $minioAddr; not starting MinIO"
} else {
    Invoke-ServiceStart 'minio' (Join-Path $BinDir 'minio.exe') @('server', $minioData, '--address', $minioAddr, '--console-address', $minioConsole) @{
        MINIO_ROOT_USER = $minioUser; MINIO_ROOT_PASSWORD = $minioPass; MINIO_BROWSER_REDIRECT = 'false'
    } | Out-Null
}
if (Wait-Until { Test-Http "http://$minioAddr/minio/health/live" } 'MinIO' 60) {
    $mc = Join-Path $BinDir 'mc.exe'
    $env:MC_CONFIG_DIR = Join-Path $DataDir 'mc'
    & $mc alias set kapsora-local "http://$minioAddr" $minioUser $minioPass --api S3v4 2>&1 | Out-Null
    foreach ($b in $buckets) {
        & $mc mb --ignore-existing "kapsora-local/$b" 2>&1 | Out-Null
    }
    & $mc version enable kapsora-local/immutable 2>&1 | Out-Null
    & $mc version enable kapsora-local/fiscal 2>&1 | Out-Null
    Write-Ok ("buckets: " + ($buckets -join ', ') + " (versioning on immutable, fiscal)")
}

Write-Step "ClamAV"
$clamHome = (Get-Content (Join-Path $ClamDir 'home.txt') -Raw).Trim()
$clamDb = Join-Path $DataDir 'clamav-db'
$mainDb = Get-ChildItem -Path $clamDb -Filter 'main.c*' -ErrorAction SilentlyContinue | Select-Object -First 1
$stale = ($null -eq $mainDb) -or ((Get-Date) - $mainDb.LastWriteTime).TotalHours -gt 24
if ($stale -and -not $SkipFreshclam) {
    Write-Step "freshclam: updating signatures (first run downloads ~300 MB, be patient)"
    $freshConf = Join-Path $ClamDir 'freshclam.conf'
    & (Join-Path $clamHome 'freshclam.exe') "--config-file=$freshConf" 2>&1 | Tee-Object -FilePath (Join-Path $RunDir 'freshclam.out.log') | Select-Object -Last 3
}
if ($null -eq (Get-ChildItem -Path $clamDb -Filter 'main.c*' -ErrorAction SilentlyContinue)) {
    Write-Warn "no signature database in tools\data\clamav-db; clamd will not start (run again without -SkipFreshclam)"
} else {
    # Build the list first: in PowerShell the comma binds tighter than +.
    $clamConf = Join-Path $ClamDir 'clamd.conf'
    $clamArgs = @("--config-file=$clamConf", '--foreground')
    Invoke-ServiceStart 'clamd' (Join-Path $clamHome 'clamd.exe') $clamArgs | Out-Null
    $clamHost, $clamPort = $clamAddr.Split(':')
    Wait-Until { Test-ClamdPing $clamHost ([int]$clamPort) } 'clamd' 180 | Out-Null
}

Write-Step "Mailpit"
$mailData = Join-Path $DataDir 'mailpit'
Initialize-Directory $mailData
Invoke-ServiceStart 'mailpit' (Join-Path $BinDir 'mailpit.exe') @('--listen', $mailUi, '--smtp', $smtpAddr, '--database', (Join-Path $mailData 'mailpit.db')) | Out-Null
Wait-Until { Test-Http "http://$mailUi/livez" } 'Mailpit' 30 | Out-Null

Write-Step "summary"
Write-Host ("  MinIO      S3 http://{0}   console http://{1}   user {2}" -f $minioAddr, $minioConsole, $minioUser)
Write-Host ("  ClamAV     clamd tcp://{0}" -f $clamAddr)
Write-Host ("  Mailpit    UI http://{0}   SMTP {1}" -f $mailUi, $smtpAddr)
Write-Host ("  PostgreSQL {0}:{1}" -f $pgHost, $pgPort)
Write-Host "  Logs and pid files: tools\run\   Stop everything: .\scripts\native\down.ps1"
