# Everything a demo needs, in one window and behind one address.
#   .\scripts\dev.ps1 up      starts the API, worker, scheduler and three apps
#   .\scripts\dev.ps1 down    stops the processes owned by this launcher
param(
    [int]$Door = 5181,
    [int]$ProviderPort = 5182,
    [int]$MemberPort = 5183
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'dev-processes.ps1')
Set-Location $root

$addr = $env:KAPSORA_HTTP_ADDR
if (-not $addr) { $addr = ':8080' }
if ($addr.StartsWith(':')) { $api = 'http://127.0.0.1' + $addr } else { $api = 'http://' + $addr }

$goPart = {
    param($root, $command, $gate)
    [pscustomobject]@{ DevRoot = $true; ProcessId = $PID; Created = [System.Diagnostics.Process]::GetCurrentProcess().StartTime.ToUniversalTime().Ticks }
    $deadline = [datetime]::UtcNow.AddSeconds(20)
    while (-not (Test-Path -LiteralPath $gate) -and -not (Test-Path -LiteralPath (Join-Path (Split-Path $gate) "stop.signal")) -and [datetime]::UtcNow -lt $deadline) {
        Start-Sleep -Milliseconds 50
    }
    if (Test-Path -LiteralPath (Join-Path (Split-Path $gate) 'stop.signal')) { return }
    if (-not (Test-Path -LiteralPath $gate)) { throw 'Dev job was not assigned to its process group.' }
    Set-Location $root
    go run ./cmd/$command
}
$webPart = {
    param($root, $api, $filter, $port, $basePath, $door, $gate)
    [pscustomobject]@{ DevRoot = $true; ProcessId = $PID; Created = [System.Diagnostics.Process]::GetCurrentProcess().StartTime.ToUniversalTime().Ticks }
    $deadline = [datetime]::UtcNow.AddSeconds(20)
    while (-not (Test-Path -LiteralPath $gate) -and -not (Test-Path -LiteralPath (Join-Path (Split-Path $gate) "stop.signal")) -and [datetime]::UtcNow -lt $deadline) {
        Start-Sleep -Milliseconds 50
    }
    if (Test-Path -LiteralPath (Join-Path (Split-Path $gate) 'stop.signal')) { return }
    if (-not (Test-Path -LiteralPath $gate)) { throw 'Dev job was not assigned to its process group.' }
    Set-Location $root
    $env:VITE_API_MOCK = 'false'
    $env:VITE_API_BASE_URL = $api
    $env:VITE_BACKOFFICE_URL = '/'
    $env:VITE_PROVIDER_URL = '/portal/'
    $env:VITE_MEMBER_URL = '/uye/'
    if ($basePath) {
        $env:VITE_BASE_PATH = $basePath
        $env:VITE_DEV_DOOR_PORT = "$door"
    }
    pnpm.cmd --filter $filter exec vite --port $port --strictPort --host 127.0.0.1
}

Write-Host "KAPSORA: API $api, tek kapı http://127.0.0.1:$Door (Ctrl+C ile hepsi durur)" -ForegroundColor Cyan
$runsPath = Join-Path $root 'tools\dev-runs'
$runPath = Join-Path $runsPath ([guid]::NewGuid().ToString('N'))
[void](New-Item -ItemType Directory -Path $runPath -Force)
$statePath = Join-Path $runPath 'state.json'
$stopPath = Join-Path $runPath 'stop.signal'
$jobName = 'Local\KapsoraDev-' + [guid]::NewGuid().ToString('N')
$owner = Get-DevProcessIdentity $PID
[System.IO.File]::WriteAllText($statePath, (ConvertTo-Json -Compress -InputObject @{ JobName = $jobName; Supervisor = $owner }), (New-Object System.Text.UTF8Encoding($false)))
$jobHandle = [IntPtr]::Zero
$parts = New-Object System.Collections.ArrayList
try {
    $jobHandle = [KapsoraDevJob]::Create($jobName)
    $definitions = @(
        @{ Name = 'api';       Script = $goPart;  Arguments = @($root, 'api') },
        @{ Name = 'worker';    Script = $goPart;  Arguments = @($root, 'worker') },
        @{ Name = 'scheduler'; Script = $goPart;  Arguments = @($root, 'scheduler') },
        @{ Name = 'panel';     Script = $webPart; Arguments = @($root, $api, '@kapsora/backoffice', $Door, $null, $Door) },
        @{ Name = 'portal';    Script = $webPart; Arguments = @($root, $api, '@kapsora/provider', $ProviderPort, '/portal/', $Door) },
        @{ Name = 'uye';       Script = $webPart; Arguments = @($root, $api, '@kapsora/member', $MemberPort, '/uye/', $Door) }
    )
    foreach ($definition in $definitions) {
        if (Test-Path -LiteralPath $stopPath) { break }
        $gate = Join-Path $runPath ($definition.Name + '.gate')
        $arguments = @($definition.Arguments) + @($gate)
        $job = Start-Job -ScriptBlock $definition.Script -ArgumentList $arguments
        [void]$parts.Add(@{ Name = $definition.Name; Job = $job })
        $deadline = [datetime]::UtcNow.AddSeconds(15)
        $identity = $null
        while (-not $identity -and -not (Test-Path -LiteralPath $stopPath) -and [datetime]::UtcNow -lt $deadline) {
            foreach ($line in @(Receive-Job -Job $job -ErrorAction SilentlyContinue)) {
                if ($line.DevRoot -eq $true) {
                    $identity = [pscustomobject]@{ Id = [int]$line.ProcessId; Created = [long]$line.Created }
                } else {
                    Write-Host ("[{0,-9}] {1}" -f $definition.Name, $line)
                }
            }
            if (-not $identity) { Start-Sleep -Milliseconds 50 }
        }
        if (Test-Path -LiteralPath $stopPath) { break }
        if (-not $identity) { throw "Dev job $($definition.Name) did not register within fifteen seconds." }
        [KapsoraDevJob]::Assign($jobHandle, [int]$identity.Id, [long]$identity.Created)
        [System.IO.File]::WriteAllText($gate, '')
    }
    while (-not (Test-Path -LiteralPath $stopPath)) {
        foreach ($part in $parts) {
            foreach ($line in @(Receive-Job -Job $part.Job -ErrorAction SilentlyContinue)) {
                Write-Host ("[{0,-9}] {1}" -f $part.Name, $line)
            }
        }
        Start-Sleep -Milliseconds 400
    }
} finally {
    Write-Host 'KAPSORA: durduruluyor...' -ForegroundColor Cyan
    # File signaling must never prevent native process-group cleanup.
    try { [System.IO.File]::WriteAllText($stopPath, '') } catch {
        Write-Warning 'Durdurma işareti yazılamadı; süreç grubu kapatılıyor.'
    }
    if ($jobHandle -ne [IntPtr]::Zero) {
        try { [void][KapsoraDevJob]::Terminate($jobHandle) }
        finally { [KapsoraDevJob]::Close($jobHandle) }
    }
    if ($parts.Count -gt 0) {
        $jobs = @($parts | ForEach-Object { $_.Job })
        Wait-Job -Job $jobs -Timeout 2 -ErrorAction SilentlyContinue | Out-Null
        foreach ($part in $parts) {
            if ($part.Job.State -in @('Completed', 'Failed', 'Stopped')) {
                Remove-Job -Job $part.Job -ErrorAction SilentlyContinue
            }
        }
    }
}
