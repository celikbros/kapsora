# Stops whatever `.\scripts\dev.ps1 up` left running: the three dev servers on 5181-5183 and the
# Go services started with `go run ./cmd/...`.
#
# The filters are deliberately narrow. A dev server is only stopped when its command line names
# one of those ports and a path inside this repository, and a Go service only when it is the
# temporary binary `go run` builds for cmd/api, cmd/worker or cmd/scheduler -- so another
# project's server on another port, and anything else on this machine, is left alone.
param([switch]$Quiet)

$ErrorActionPreference = "Stop"
$root = (Split-Path -Parent $PSScriptRoot).ToLowerInvariant()

$stopped = 0
$candidates = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
    $line = $_.CommandLine
    if (-not $line) { return $false }
    $lower = $line.ToLowerInvariant()
    $isWeb = ($_.Name -eq 'node.exe' -or $_.Name -eq 'cmd.exe') -and
             ($lower -cmatch '--port\s+518[123]\b') -and ($lower.Contains('kapsora'))
    $isGo = ($lower.Contains('go-build') -or $lower.Contains('\cmd\')) -and
            ($_.Name -eq 'api.exe' -or $_.Name -eq 'worker.exe' -or $_.Name -eq 'scheduler.exe')
    $isGoRun = ($_.Name -eq 'go.exe') -and ($lower -cmatch 'run\s+\./cmd/(api|worker|scheduler)')
    $isWeb -or $isGo -or $isGoRun
}
foreach ($process in $candidates) {
    Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue
    $stopped++
}
if (-not $Quiet) {
    Write-Host ("KAPSORA durduruldu ({0} surec)." -f $stopped)
}
