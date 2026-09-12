# Everything a demo needs, in one window and behind one address.
#
#   .\scripts\dev.ps1 up      starts the API, the worker, the scheduler and the three apps
#   .\scripts\dev.ps1 down    stops whatever it left behind
#
# The three apps are one origin here, exactly as they are in a deployment: the backoffice dev
# server is the door on 5181, and it hands /portal/ and /uye/ through to the other two servers,
# hot-reload sockets included. One origin is one session cookie, which is what makes the single
# sign-in single -- and what keeps a port out of every address a person types.
#
# .env is already loaded into this process by dev.ps1.
param(
    [int]$Door = 5181,
    [int]$ProviderPort = 5182,
    [int]$MemberPort = 5183
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$addr = $env:KAPSORA_HTTP_ADDR
if (-not $addr) { $addr = ":8080" }
if ($addr.StartsWith(":")) { $api = "http://127.0.0.1" + $addr } else { $api = "http://" + $addr }

$goPart = {
    param($root, $command)
    Set-Location $root
    go run ./cmd/$command
}

$webPart = {
    param($root, $api, $filter, $port, $basePath, $door)
    Set-Location $root
    $env:VITE_API_MOCK = 'false'
    $env:VITE_API_BASE_URL = $api
    # Behind the door the apps are one origin apart, so the single sign-in hands a person to
    # another app by path rather than by port.
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

$parts = @(
    @{ Name = "api";       Job = Start-Job -ScriptBlock $goPart -ArgumentList $root, "api" },
    @{ Name = "worker";    Job = Start-Job -ScriptBlock $goPart -ArgumentList $root, "worker" },
    @{ Name = "scheduler"; Job = Start-Job -ScriptBlock $goPart -ArgumentList $root, "scheduler" },
    @{ Name = "panel";     Job = Start-Job -ScriptBlock $webPart -ArgumentList $root, $api, "@kapsora/backoffice", $Door, $null, $Door },
    @{ Name = "portal";    Job = Start-Job -ScriptBlock $webPart -ArgumentList $root, $api, "@kapsora/provider", $ProviderPort, "/portal/", $Door },
    @{ Name = "uye";       Job = Start-Job -ScriptBlock $webPart -ArgumentList $root, $api, "@kapsora/member", $MemberPort, "/uye/", $Door }
)

try {
    while ($true) {
        foreach ($part in $parts) {
            foreach ($line in (Receive-Job -Job $part.Job -ErrorAction SilentlyContinue)) {
                Write-Host ("[{0,-9}] {1}" -f $part.Name, $line)
            }
        }
        Start-Sleep -Milliseconds 400
    }
} finally {
    Write-Host "KAPSORA: durduruluyor..." -ForegroundColor Cyan
    foreach ($part in $parts) {
        Stop-Job -Job $part.Job -ErrorAction SilentlyContinue
        Remove-Job -Job $part.Job -Force -ErrorAction SilentlyContinue
    }
    & (Join-Path $PSScriptRoot "dev-down.ps1") -Quiet
}
