param(
    [Parameter(Mandatory=$true)][int]$HighProcessId,
    [Parameter(Mandatory=$true)][string]$ResultPath
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'WorkerProfile.ps1')
$receipt = [ordered]@{ actual_process=[Diagnostics.Process]::GetCurrentProcess().Id; token=[HarborUIProfile]::CurrentTokenFacts(); complete=$false; rights=@() }
$exitCode = 1
try {
    [HarborUIProfile]::ProveNativeProcessRights($HighProcessId)
    $receipt.complete = $true
    $exitCode = 0
} catch {
    $receipt.error = $_.Exception.ToString()
} finally {
    $receipt.rights = @([HarborUIProfile]::AccessReceipts)
    $receipt | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $ResultPath -Encoding utf8
}
exit $exitCode
