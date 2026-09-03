<#
.SYNOPSIS
  Health of PostgreSQL, MinIO, ClamAV, Mailpit and the KAPSORA API. Exit code 1 when a
  required service is down (MinIO, ClamAV, Mailpit are optional for the current modules).
#>
[CmdletBinding()]
param()

. "$PSScriptRoot\_common.ps1"
Import-DotEnv

$minioAddr = Get-EnvOrDefault 'KAPSORA_MINIO_ADDR' '127.0.0.1:9000'
$clamAddr = Get-EnvOrDefault 'KAPSORA_CLAMAV_ADDR' '127.0.0.1:3310'
$mailUi = Get-EnvOrDefault 'KAPSORA_MAILPIT_UI_ADDR' '127.0.0.1:8025'
$apiAddr = Get-EnvOrDefault 'KAPSORA_HTTP_ADDR' ':8080'
if ($apiAddr.StartsWith(':')) { $apiAddr = "127.0.0.1$apiAddr" }

$pgHost = '127.0.0.1'; $pgPort = 5432
if ($env:KAPSORA_DATABASE_URL -match '@([^:/]+):(\d+)/') { $pgHost = $matches[1]; $pgPort = [int]$matches[2] }
$clamHost, $clamPort = $clamAddr.Split(':')

$rows = @(
    @{ Name = 'PostgreSQL'; Required = $true; Where = "${pgHost}:${pgPort}"; Up = (Test-TcpPort $pgHost $pgPort) }
    @{ Name = 'MinIO'; Required = $false; Where = "http://$minioAddr"; Up = (Test-Http "http://$minioAddr/minio/health/live") }
    @{ Name = 'ClamAV'; Required = $false; Where = "tcp://$clamAddr"; Up = (Test-ClamdPing $clamHost ([int]$clamPort)) }
    @{ Name = 'Mailpit'; Required = $false; Where = "http://$mailUi"; Up = (Test-Http "http://$mailUi/livez") }
    @{ Name = 'KAPSORA API'; Required = $false; Where = "http://$apiAddr"; Up = (Test-Http "http://$apiAddr/health/ready") }
)

$failed = $false
foreach ($r in $rows) {
    $state = 'DOWN'; $color = 'Red'
    if ($r.Up) { $state = 'UP'; $color = 'Green' } elseif (-not $r.Required) { $color = 'Yellow' }
    Write-Host ("  {0,-12} {1,-5} {2}" -f $r.Name, $state, $r.Where) -ForegroundColor $color
    if (-not $r.Up -and $r.Required) { $failed = $true }
}
foreach ($name in @('minio', 'clamd', 'mailpit')) {
    $p = Get-RunningProcess $name
    if ($null -ne $p) { Write-Host ("  {0,-12} pid {1}" -f $name, $p.Id) -ForegroundColor DarkGray }
}
if ($failed) { exit 1 }
