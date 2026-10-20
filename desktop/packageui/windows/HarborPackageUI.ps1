param(
    [Parameter(Mandatory=$true)][string]$InstallRoot,
    [Parameter(Mandatory=$true)][string]$EvidenceDirectory,
    [Parameter(Mandatory=$true)][string]$ToolsPath
)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes, System.Windows.Forms, System.Drawing
$desktopPath = Join-Path $InstallRoot 'harbor-desktop.exe'
$cliPath = Join-Path $InstallRoot 'harbor.exe'
$projectRoot = Join-Path $env:TEMP ('harbor-installed-ui-' + [Guid]::NewGuid().ToString('N'))
$projectDirectory = Join-Path $projectRoot 'project'
New-Item -ItemType Directory -Path $projectRoot | Out-Null
$script:window = $null

function Wait-Until([string]$Description, [scriptblock]$Condition, [int]$Seconds=30) {
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    do {
        if (& $Condition) { return }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Timed out: $Description"
}
function Get-Snapshot {
    $output = & $cliPath daemon snapshot
    if ($LASTEXITCODE -ne 0) { throw 'installed CLI snapshot failed' }
    return ($output | ConvertFrom-Json)
}
function Get-Project {
    return @((Get-Snapshot).projects | Where-Object { $_.path -ceq $projectDirectory }) | Select-Object -First 1
}
function Find-Control([string]$Name, [System.Windows.Automation.ControlType]$Type) {
    $conditions = [System.Windows.Automation.Condition[]]@(
        [System.Windows.Automation.PropertyCondition]::new([System.Windows.Automation.AutomationElement]::NameProperty, $Name),
        [System.Windows.Automation.PropertyCondition]::new([System.Windows.Automation.AutomationElement]::ControlTypeProperty, $Type)
    )
    return $script:window.FindFirst([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.AndCondition]::new($conditions))
}
function Await-Control([string]$Name, [System.Windows.Automation.ControlType]$Type) {
    Wait-Until "enabled control $Name" { $control = Find-Control $Name $Type; $null -ne $control -and $control.Current.IsEnabled }
    return Find-Control $Name $Type
}
function Invoke-Button([string]$Name) {
    $control = Await-Control $Name ([System.Windows.Automation.ControlType]::Button)
    $control.GetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern).Invoke()
}
function Set-Field([string]$Name, [string]$Value) {
    $control = Await-Control $Name ([System.Windows.Automation.ControlType]::Edit)
    $control.SetFocus()
    $control.GetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern).SetValue($Value)
}
function Set-Checkbox([string]$Name, [bool]$Selected) {
    $control = Await-Control $Name ([System.Windows.Automation.ControlType]::CheckBox)
    $pattern = $control.GetCurrentPattern([System.Windows.Automation.TogglePattern]::Pattern)
    if (($pattern.Current.ToggleState -eq [System.Windows.Automation.ToggleState]::On) -ne $Selected) { $pattern.Toggle() }
    Wait-Until "checkbox $Name selection" { ($pattern.Current.ToggleState -eq [System.Windows.Automation.ToggleState]::On) -eq $Selected }
}
function Set-Selection([string]$Name, [string]$Value) {
    $control = Await-Control $Name ([System.Windows.Automation.ControlType]::ComboBox)
    $control.SetFocus()
    [System.Windows.Forms.SendKeys]::SendWait($Value + '{ENTER}')
}
function Open-Harbor {
    $priorPath = $env:PATH
    try {
        $env:PATH = "$ToolsPath;$env:PATH"
        $launched = [HarborUIProfile]::Launch($desktopPath, $InstallRoot)
    } finally { $env:PATH = $priorPath }
    Wait-Until 'one installed desktop process' { @(Get-Process -Name harbor-desktop -ErrorAction SilentlyContinue).Count -eq 1 } 60
    $process = Get-Process -Name harbor-desktop
    if ($process.Path -cne $desktopPath) { throw 'desktop process is outside admitted installation' }
    [HarborUIProfile]::AssertProductProcess($process.Id)
    Wait-Until 'visible installed window' {
        $process.Refresh()
        if ($process.MainWindowHandle -eq [IntPtr]::Zero) { return $false }
        $script:window = [System.Windows.Automation.AutomationElement]::FromHandle($process.MainWindowHandle)
        return $null -ne $script:window -and -not $script:window.Current.IsOffscreen
    } 60
    return $process.Id
}
function Await-ProjectState([string]$Expected) {
    Wait-Until "project state $Expected" {
        $project = Get-Project
        if ($null -eq $project) { return $false }
        if ($project.state -in @('failed', 'quarantined')) { throw "project entered $($project.state)" }
        return $project.state -ceq $Expected
    } 240
}
function Assert-HTTPS {
    $project = Get-Project
    $response = Invoke-WebRequest -Uri "https://$($project.slug).test" -UseBasicParsing -TimeoutSec 15
    if ($response.StatusCode -ne 200) { throw 'system-trusted HTTPS did not return 200' }
}
function Save-Observation([string]$Name) {
    try {
      if ($null -ne $script:window) {
        $controls = $script:window.FindAll([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.Condition]::TrueCondition)
        $facts = for ($index=0; $index -lt [Math]::Min($controls.Count, 3000); $index++) {
            $current = $controls.Item($index).Current
            [ordered]@{ name=$current.Name; type=$current.ControlType.ProgrammaticName; enabled=$current.IsEnabled; offscreen=$current.IsOffscreen }
        }
        $facts | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $EvidenceDirectory "$Name-controls.json") -Encoding utf8
    }
    } catch { Write-Warning "control diagnostics failed: $_" }
    try {
    $bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
    $bitmap = [System.Drawing.Bitmap]::new($bounds.Width, $bounds.Height)
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    try {
        $graphics.CopyFromScreen($bounds.Location, [System.Drawing.Point]::Empty, $bounds.Size)
        $bitmap.Save((Join-Path $EvidenceDirectory "$Name.png"), [System.Drawing.Imaging.ImageFormat]::Png)
    } finally { $graphics.Dispose(); $bitmap.Dispose() }
    } catch { Write-Warning "screenshot diagnostics failed: $_" }
    try { Get-Snapshot | ConvertTo-Json -Depth 30 | Set-Content (Join-Path $EvidenceDirectory "$Name-snapshot.json") -Encoding utf8 } catch { Write-Warning $_ }
}

. (Join-Path $PSScriptRoot 'WorkerProfile.ps1')
Assert-InteractiveWorker
try {
    $originalProcess = Open-Harbor
    Wait-Until 'medium-integrity installed daemon' { @(Get-Process -Name harbord -ErrorAction SilentlyContinue).Count -eq 1 } 60
    $daemonProcess = Get-Process -Name harbord
    $null = $daemonProcess.Handle
    [HarborUIProfile]::AssertProductProcess($daemonProcess.Id)
    if ($daemonProcess.Path -cne (Join-Path $InstallRoot 'harbord.exe')) { throw 'daemon process is outside admitted installation' }
    Save-Observation 'installed-first-launch'
    Wait-Until 'setup action or configured networking' {
        $null -ne (Find-Control 'Set up secure networking' ([System.Windows.Automation.ControlType]::Button)) -or (Get-Snapshot).network_setup.configured -eq $true
    } 30
    $setup = Find-Control 'Set up secure networking' ([System.Windows.Automation.ControlType]::Button)
    if ($null -ne $setup) { Invoke-Button 'Set up secure networking' }
    Wait-Until 'secure networking configured' { (Get-Snapshot).network_setup.configured -eq $true } 120
    Invoke-Button 'New project'
    Set-Field 'Project name' 'Harbor Installed Windows UI'
    Set-Field 'Go module' 'example.com/harbor-installed-windows-ui'
    Set-Field 'Project directory' $projectDirectory
    Invoke-Button 'Continue'
    foreach ($name in @('CLI','Docker','Mail','Auth','OAuth','Web API','Web UI','Metrics','Observability','Grafana','Database (MySQL)','Database (Postgres)','Database (SQLite)','Scheduler','Cache','Events','File Storage','Background Jobs')) {
        Set-Checkbox $name ($name -in @('CLI','Web API','Web UI'))
    }
    Set-Selection 'Starter kit' 'Vue'
    Invoke-Button 'Continue'
    Set-Selection 'Atlas agent support' 'Skip'
    Invoke-Button 'Continue'
    Save-Observation 'creation-review'
    Invoke-Button 'Create project'
    Await-ProjectState 'stopped'
    Invoke-Button 'Start project'
    Await-ProjectState 'ready'
    Assert-HTTPS
    Save-Observation 'project-ready'
    $script:window.GetCurrentPattern([System.Windows.Automation.WindowPattern]::Pattern).Close()
    Wait-Until 'close hides the installed window' { $script:window.Current.IsOffscreen }
    if ((Get-Project).state -cne 'ready') { throw 'closing the UI stopped the project' }
    if ((Open-Harbor) -ne $originalProcess) { throw 'reopening did not restore the original desktop process' }
    $script:window.SetFocus()
    [System.Windows.Forms.SendKeys]::SendWait('^q')
    Wait-Until 'UI-only quit exits desktop' { $null -eq (Get-Process -Id $originalProcess -ErrorAction SilentlyContinue) }
    if ((Get-Project).state -cne 'ready') { throw 'UI-only quit stopped the project' }
    $reopenedProcess = Open-Harbor
    Await-ProjectState 'ready'
    Assert-HTTPS
    Save-Observation 'reopened-running-project'
    if ($null -eq (Find-Control 'Stop project' ([System.Windows.Automation.ControlType]::Button))) {
        Invoke-Button 'Projects'
        $projectLink = Await-Control 'Harbor Installed Windows UI' ([System.Windows.Automation.ControlType]::Hyperlink)
        $projectLink.GetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern).Invoke()
    }
    Invoke-Button 'Stop project'
    Await-ProjectState 'stopped'
    $script:window.SetFocus()
    [System.Windows.Forms.SendKeys]::SendWait('^q')
    Wait-Until 'final desktop quit' { $null -eq (Get-Process -Id $reopenedProcess -ErrorAction SilentlyContinue) }
    [ordered]@{
        application=$desktopPath; project_directory=$projectDirectory; project_id=(Get-Project).id
        creation='production graphical wizard through native UIAutomation'; start='ready and system-trusted HTTPS 200'
        window_close='project remains ready'; second_launch='original process restored'
        ui_quit='daemon and project remain ready'; ui_reopen='registration retained and HTTPS 200'; final_project_state='stopped'
    } | ConvertTo-Json | Set-Content (Join-Path $EvidenceDirectory 'installed-ui.json') -Encoding utf8
} catch {
    $originalFailure = $_
    Save-Observation 'failure'
    try { [HarborUIProfile]::QuiesceLaunchedUIs() } catch { Write-Warning "failed to quiesce exact launched UI handles: $_" }
    throw $originalFailure
} finally {
    $cleanup = [ordered]@{ launched_token_facts=[HarborUIProfile]::LastLaunchFacts; gui_quiescent=$false; fixtures_stopped=$false; network_released=$false; daemon_absent=$false; retained_files=@{}; missing_files=@() }
    try {
        [HarborUIProfile]::QuiesceLaunchedUIs()
        $cleanup.gui_quiescent = $true
        $remaining = @(Get-Process -Name harbor-desktop -ErrorAction SilentlyContinue)
        if ($remaining.Count -ne 0) { throw 'an unrecorded desktop process prevents host cleanup' }
        $registered = Get-Project
        if ($null -ne $registered -and $registered.state -cne 'stopped') {
            & $cliPath stop $registered.id
            if ($LASTEXITCODE -ne 0) { throw 'fixture stop was not admitted' }
            Wait-Until 'stopped cleanup fixture' { (Get-Project).state -ceq 'stopped' } 120
        }
        $cleanup.fixtures_stopped = $true
        & $cliPath daemon release
        if ($LASTEXITCODE -ne 0) { throw 'network release did not complete' }
        $cleanup.network_released = $true
        & $cliPath daemon stop
        if ($LASTEXITCODE -ne 0) { throw 'daemon shutdown was not acknowledged' }
        if ($null -ne $daemonProcess) {
            Wait-Until 'exact installed daemon process exit' { $daemonProcess.HasExited } 30
        }
        if (@(Get-Process -Name harbord -ErrorAction SilentlyContinue).Count -ne 0) { throw 'a daemon process remains after shutdown' }
        $cleanup.daemon_absent = $true
        $paths = @((Join-Path $env:LOCALAPPDATA 'GoForj\Harbor\harbor.db'), (Join-Path $projectDirectory '.goforj.yml'), (Join-Path $projectDirectory 'go.mod'))
        foreach ($path in $paths) {
            if (Test-Path -LiteralPath $path -PathType Leaf) {
                $cleanup.retained_files[$path] = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash
            } else {
                $cleanup.missing_files += $path
                if (Test-Path (Join-Path $EvidenceDirectory 'installed-ui.json')) { throw "successful GUI lifecycle lost required retained file $path" }
            }
        }
    } catch { $cleanup.error = $_.ToString(); Write-Warning "partial installed cleanup: $_" }
    $cleanup | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $EvidenceDirectory 'cleanup.json') -Encoding utf8
    [HarborUIProfile]::CloseLaunchReceipts()
}
