# Each run uses a copied root and fake go/pnpm commands; no application service starts.
# Signal exercises the same finally block as Ctrl+C (literal console Ctrl+C is manual).
$originalPath = $env:PATH
$originalPids = $env:DEV_TEST_PIDS
try {
    foreach ($mode in @("Down", "Signal", "Early")) {
        $ErrorActionPreference = 'Stop'
        $repo = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
        $fixture = Join-Path $repo ('tools\dev-job-fixture-' + [guid]::NewGuid().ToString('N'))
        $bin = Join-Path $fixture 'bin'
        $scripts = Join-Path $fixture 'scripts'
        $pids = Join-Path $fixture 'pids'
        [void](New-Item -ItemType Directory -Path $bin,$scripts,$pids -Force)
        Copy-Item (Join-Path $repo 'scripts\dev-up.ps1') $scripts
        Copy-Item (Join-Path $repo 'scripts\dev-down.ps1') $scripts
        Copy-Item (Join-Path $repo 'scripts\dev-processes.ps1') $scripts
        Set-Content -LiteralPath (Join-Path $bin 'go.cmd') -Encoding Ascii -Value @('@echo off','powershell.exe -NoProfile -File "%~dp0mock-worker.ps1"')
        Set-Content -LiteralPath (Join-Path $bin 'pnpm.cmd') -Encoding Ascii -Value @('@echo off','powershell.exe -NoProfile -File "%~dp0mock-worker.ps1"')
        Set-Content -LiteralPath (Join-Path $bin 'mock-worker.ps1') -Encoding UTF8 -Value @(
            '$child = Start-Process powershell.exe -ArgumentList @("-NoProfile", "-Command", "Start-Sleep -Seconds 90") -PassThru -WindowStyle Hidden',
            '[System.IO.File]::WriteAllText((Join-Path $env:DEV_TEST_PIDS ("child-" + $PID + ".txt")), [string]$child.Id)'
        )
        $env:PATH = $bin + ';' + $env:PATH
        $env:DEV_TEST_PIDS = $pids
        $unrelated = Start-Process powershell.exe -ArgumentList @('-NoProfile','-Command','Start-Sleep -Seconds 90') -PassThru -WindowStyle Hidden
        $launcher = $null
        try {
            $launcher = Start-Process powershell.exe -ArgumentList @('-NoProfile','-File',('"' + (Join-Path $scripts 'dev-up.ps1') + '"')) -PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $fixture 'up.out') -RedirectStandardError (Join-Path $fixture 'up.err')
            if ($mode -eq 'Early') {
                $deadline = [datetime]::UtcNow.AddSeconds(15)
                $run = $null
                while (-not $run -and [datetime]::UtcNow -lt $deadline -and -not $launcher.HasExited) {
                    $run = @(Get-ChildItem -LiteralPath (Join-Path $fixture 'tools\dev-runs') -Directory -ErrorAction SilentlyContinue | Where-Object { Test-Path -LiteralPath (Join-Path $_.FullName 'api.gate') } | Select-Object -First 1)[0]
                    if (-not $run) { Start-Sleep -Milliseconds 20 }
                }
                if (-not $run) { throw 'First job gate did not appear.' }
                $earlyClock = [Diagnostics.Stopwatch]::StartNew()
                [System.IO.File]::WriteAllText((Join-Path $run.FullName 'stop.signal'), '')
                if (-not $launcher.WaitForExit(5000)) { throw 'Early stop did not exit within five seconds.' }
                $earlyClock.Stop()
                foreach ($file in @(Get-ChildItem -LiteralPath $pids -File)) {
                    $childId = [int](Get-Content $file.FullName)
                    if (Get-Process -Id $childId -ErrorAction SilentlyContinue) { throw "Early owned descendant $childId survived." }
                }
                if (-not (Get-Process -Id $unrelated.Id -ErrorAction SilentlyContinue)) { throw 'Early stop killed unrelated process.' }
                Write-Host ("FIXTURE_OK mode=Early duration_ms={0}" -f $earlyClock.ElapsedMilliseconds)
                continue
            }
            $deadline = [datetime]::UtcNow.AddSeconds(35)
            while (@(Get-ChildItem -LiteralPath $pids -File).Count -lt 6 -and [datetime]::UtcNow -lt $deadline -and -not $launcher.HasExited) {
                Start-Sleep -Milliseconds 200
            }
            $pidFiles = @(Get-ChildItem -LiteralPath $pids -File)
            if ($pidFiles.Count -ne 6) {
                Write-Host ('UP_OUT: ' + (Get-Content (Join-Path $fixture 'up.out') -Raw))
                Write-Host ('UP_ERR: ' + (Get-Content (Join-Path $fixture 'up.err') -Raw))
                throw "Expected six mock child processes, got $($pidFiles.Count)."
            }
            $children = @($pidFiles | ForEach-Object { [int](Get-Content $_.FullName) })
            $elapsed = [Diagnostics.Stopwatch]::StartNew()
            if ($mode -eq 'Down') {
                & (Join-Path $scripts 'dev-down.ps1')
            } else {
                $activeRun = @(Get-ChildItem -LiteralPath (Join-Path $fixture 'tools\dev-runs') -Directory | Select-Object -First 1)[0]
                if (-not $activeRun) { throw 'No active run manifest.' }
                [System.IO.File]::WriteAllText((Join-Path $activeRun.FullName 'stop.signal'), '')
            }
            if (-not $launcher.WaitForExit(5000)) { throw 'Launcher did not exit within five seconds.' }
            $elapsed.Stop()
            foreach ($childId in $children) {
                Start-Sleep -Milliseconds 50
                if (Get-Process -Id $childId -ErrorAction SilentlyContinue) { throw "Owned descendant $childId survived." }
            }
            if (-not (Get-Process -Id $unrelated.Id -ErrorAction SilentlyContinue)) { throw 'Unrelated process was stopped.' }
            $stalePath = Join-Path $fixture 'tools\dev-runs\stale'
            [void](New-Item -ItemType Directory -Path $stalePath -Force)
            $fake = @{ JobName = 'Local\KapsoraDev-' + [guid]::NewGuid().ToString('N'); Supervisor = @{ Id = $unrelated.Id; Created = 0 } }
            [System.IO.File]::WriteAllText((Join-Path $stalePath 'state.json'), (ConvertTo-Json -Compress $fake))
            & (Join-Path $scripts 'dev-down.ps1')
            if (Test-Path -LiteralPath (Join-Path $stalePath 'stop.signal')) { throw 'Stale PID record was signaled.' }
            Write-Host ("FIXTURE_OK mode={3} duration_ms={0} children={1} unrelated={2}" -f $elapsed.ElapsedMilliseconds,$children.Count,$unrelated.Id,$mode)
        } finally {
            if ($launcher -and -not $launcher.HasExited) { try { $launcher.Kill() } catch {} }
            if ($unrelated -and -not $unrelated.HasExited) { try { $unrelated.Kill() } catch {} }
        }
    }
} finally {
    $env:PATH = $originalPath
    $env:DEV_TEST_PIDS = $originalPids
}
