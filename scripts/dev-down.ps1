# Stop only process groups created by scripts/dev-up.ps1.
param([switch]$Quiet)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'dev-processes.ps1')
$root = Split-Path -Parent $PSScriptRoot
$runsPath = Join-Path $root 'tools\dev-runs'
$stopped = 0
if (Test-Path -LiteralPath $runsPath) {
    foreach ($run in @(Get-ChildItem -LiteralPath $runsPath -Directory -ErrorAction SilentlyContinue)) {
        $statePath = Join-Path $run.FullName 'state.json'
        if (-not (Test-Path -LiteralPath $statePath)) { continue }
        try { $state = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json } catch { continue }
        if ($state.JobName -cnotmatch '^Local\\KapsoraDev-[0-9a-f]{32}$') { continue }
        if (Test-DevProcessIdentity $state.Supervisor) {
            [System.IO.File]::WriteAllText((Join-Path $run.FullName 'stop.signal'), '')
        }
        $jobHandle = [KapsoraDevJob]::Open([string]$state.JobName)
        if ($jobHandle -eq [IntPtr]::Zero) { continue }
        try {
            [void][KapsoraDevJob]::Terminate($jobHandle)
            $stopped++
        } finally {
            [KapsoraDevJob]::Close($jobHandle)
        }
    }
}
if (-not $Quiet) {
    if ($stopped -eq 0) { Write-Host "Kayıtlı etkin geliştirme oturumu bulunamadı." } else { Write-Host ("KAPSORA durduruldu ({0} oturum)." -f $stopped) }
}
