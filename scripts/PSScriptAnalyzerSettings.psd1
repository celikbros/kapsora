@{
    # Developer CLI scripts: coloured console output is the point, so Write-Host is deliberate.
    ExcludeRules = @('PSAvoidUsingWriteHost')
    Severity     = @('Error', 'Warning')
}
