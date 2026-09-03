<#
.SYNOPSIS
  Stops everything started by up.ps1 (MinIO, clamd, Mailpit). PostgreSQL is a Windows
  service and is left running.
#>
[CmdletBinding()]
param()

. "$PSScriptRoot\_common.ps1"

Write-Step "stopping native services"
foreach ($name in @('mailpit', 'clamd', 'minio')) {
    Invoke-ServiceStop $name
}
Write-Ok "done (data kept under tools\data)"
