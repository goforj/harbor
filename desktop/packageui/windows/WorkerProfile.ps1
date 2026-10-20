Add-Type -Path (Join-Path $PSScriptRoot 'WorkerProfile.cs')
[HarborUIProfile]::ProcessProbeScript = Join-Path $PSScriptRoot 'ProbeProcessRights.ps1'

function Assert-InteractiveWorker {
    $session = (Get-Process -Id $PID).SessionId
    if ($session -le 0 -or -not [Environment]::UserInteractive) { throw 'installed GUI acceptance requires a real interactive Windows session' }
    # The hosted Explorer shell may be elevated. Product launch uses a restricted token instead.
    if ($session -ne [Diagnostics.Process]::GetCurrentProcess().SessionId) { throw 'interactive session drifted' }
}
