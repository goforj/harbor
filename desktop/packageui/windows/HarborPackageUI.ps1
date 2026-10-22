param(
    [Parameter(Mandatory=$true)][string]$InstallRoot,
    [Parameter(Mandatory=$true)][string]$EvidenceDirectory,
    [Parameter(Mandatory=$true)][string]$ToolsPath
)
$ErrorActionPreference = 'Stop'
$script:approvalIndeterminate = $false
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes, System.Windows.Forms, System.Drawing
$fixedRoot = Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::ProgramFiles)) 'GoForj\Harbor'
if (-not [string]::Equals([IO.Path]::GetFullPath($InstallRoot), $fixedRoot, [StringComparison]::OrdinalIgnoreCase)) { throw 'GUI test requires the fixed admitted installation' }
$selection = Get-Content -LiteralPath (Join-Path $InstallRoot 'installation.json') -Raw | ConvertFrom-Json
$userSID = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
if ($selection.schema_version -ne 1 -or $selection.product_id -cne 'com.goforj.harbor' -or
    $selection.user_sid -cne $userSID -or $selection.installation_id -cnotmatch '^[a-f0-9]{32}$' -or
    $selection.release_sequence -le 0 -or $selection.signer_thumbprint -cnotmatch '^[A-F0-9]{40}$' -or
    $selection.bundle_digest -cnotmatch '^[a-f0-9]{64}$') { throw 'installed selection identity is incomplete or foreign' }
$releaseRelative = 'releases/' + [string]$selection.release_sequence + '/'
$manifest = Get-Content -LiteralPath (Join-Path $InstallRoot ($releaseRelative + 'release-manifest.json')) -Raw | ConvertFrom-Json
if ($manifest.schema_version -ne 1 -or $manifest.product_id -cne $selection.product_id -or
    $manifest.release_sequence -ne $selection.release_sequence -or $manifest.bundle_digest -cne $selection.bundle_digest -or
    $manifest.signer_thumbprint -cne $selection.signer_thumbprint) { throw 'installed manifest differs from committed selection' }
$rolePaths = @{
    'desktop-launcher'='desktop-launcher.exe'; 'daemon-launcher'='daemon-launcher.exe'; 'cli-launcher'='harbor.exe'
    'desktop'=($releaseRelative + 'harbor-desktop.exe'); 'daemon'=($releaseRelative + 'harbord.exe'); 'cli'=($releaseRelative + 'harbor.exe')
    'output-broker'=($releaseRelative + 'outputbroker.exe'); 'helper'='harbor-helper.exe'; 'installer'=($releaseRelative + 'harbor-installer.exe')
    'uninstaller'='Uninstall.exe'; 'task-policy'='harbor-daemon-task.ps1'; 'uninstall-policy'='harbor-uninstall.ps1'
    'signer-certificate'='harbor-helper-signing.cer'; 'signer-thumbprint'='harbor-helper-signing-thumbprint.txt'
}
if (@($manifest.components).Count -ne $rolePaths.Count) { throw 'installed manifest has a different component set' }
$seenRoles = @{}
foreach ($component in $manifest.components) {
    if (-not $rolePaths.ContainsKey($component.role) -or $seenRoles.ContainsKey($component.role) -or
        $component.path -cne $rolePaths[$component.role]) { throw 'installed component differs from its fixed role' }
    $seenRoles[$component.role] = $true
    $path = Join-Path $InstallRoot $component.path
    $item = Get-Item -LiteralPath $path -Force
    if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -or $item.Length -ne $component.size -or
        (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -cne $component.sha256) { throw 'installed component identity differs from manifest' }
    if ($item.Extension -in @('.exe', '.ps1')) {
        $signature = Get-AuthenticodeSignature -LiteralPath $path
        if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Thumbprint -cne $selection.signer_thumbprint) { throw 'installed component publisher differs from package' }
    }
}
$launcherPath = Join-Path $InstallRoot 'desktop-launcher.exe'
$desktopPath = Join-Path $InstallRoot $rolePaths['desktop']
$daemonPath = Join-Path $InstallRoot $rolePaths['daemon']
$cliPath = Join-Path $InstallRoot 'harbor.exe'
$projectRoot = Join-Path (Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)) 'Temp') ('harbor-package-ui-' + [Guid]::NewGuid().ToString('N'))
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
# Find-FolderDialog admits only the native chooser owned by the selected installed desktop process.
function Find-FolderDialog {
    $conditions = [System.Windows.Automation.Condition[]]@(
        [System.Windows.Automation.PropertyCondition]::new([System.Windows.Automation.AutomationElement]::ProcessIdProperty, [int]$script:window.Current.ProcessId),
        [System.Windows.Automation.PropertyCondition]::new([System.Windows.Automation.AutomationElement]::NameProperty, 'Choose a location for the new project'),
        [System.Windows.Automation.PropertyCondition]::new([System.Windows.Automation.AutomationElement]::ControlTypeProperty, [System.Windows.Automation.ControlType]::Window)
    )
    $matches = $script:window.FindAll(
        [System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.AndCondition]::new($conditions))
    if ($matches.Count -gt 1) { throw 'multiple native folder dialogs match the selected product' }
    if ($matches.Count -eq 1 -and -not $matches[0].Current.IsOffscreen) { return $matches[0] }
    return $null
}
# Find-DialogControl keeps native control IDs scoped to the admitted chooser rather than the web form.
function Find-DialogControl($Dialog, [ValidateSet("1","2","1152")][string]$Identifier) {
    $condition = [System.Windows.Automation.PropertyCondition]::new([System.Windows.Automation.AutomationElement]::AutomationIdProperty, $Identifier)
    $control = $Dialog.FindFirst([System.Windows.Automation.TreeScope]::Descendants, $condition)
    if ($null -eq $control -or -not $control.Current.IsEnabled) {
        $rows = @($Dialog.FindAll([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.Condition]::TrueCondition) | Select-Object -First 150 | ForEach-Object {
            @{ name=$_.Current.Name; id=$_.Current.AutomationId; type=$_.Current.ControlType.ProgrammaticName; patterns=@($_.GetSupportedPatterns() | ForEach-Object { $_.ProgrammaticName }) }
        })
        ConvertTo-Json -InputObject $rows | Set-Content (Join-Path $EvidenceDirectory 'native-folder-controls.json') -Encoding UTF8
        throw "native folder control $Identifier is missing or disabled"
    }
    return $control
}
# Invoke-DialogButton supports the legacy accessibility provider used by the native shell dialog.
function Invoke-DialogButton($Dialog, [ValidateSet("1","2")][string]$Identifier) {
    $control = Find-DialogControl $Dialog $Identifier
    $pattern = $null
    if ($control.TryGetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern, [ref]$pattern)) { $pattern.Invoke(); return }
    if ($control.TryGetCurrentPattern([System.Windows.Automation.LegacyIAccessiblePattern]::Pattern, [ref]$pattern)) { $pattern.DoDefaultAction(); return }
    throw "native folder button $Identifier has no accessible action"
}
# Set-DialogLocation targets the shell's folder-name edit through its supported native accessibility provider.
function Set-DialogLocation($Dialog, [string]$Path) {
    $control = Find-DialogControl $Dialog '1152'
    $pattern = $null
    if ($control.TryGetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern, [ref]$pattern)) { $pattern.SetValue($Path); return }
    if ($control.TryGetCurrentPattern([System.Windows.Automation.LegacyIAccessiblePattern]::Pattern, [ref]$pattern)) { $pattern.SetValue($Path); return }
    throw 'native folder location has no accessible value provider'
}
# Select-ProjectLocation checks Cancel and Select using the native folder dialog before graphical creation.
function Select-ProjectLocation {
    $previous = Join-Path $projectRoot 'unchanged-before-picker'
    Set-Field 'Project directory' $previous
    Invoke-Button 'Choose project location'
    Wait-Until 'native folder chooser' { $null -ne (Find-FolderDialog) }
    $dialog = Find-FolderDialog
    Invoke-DialogButton $dialog '2'
    Wait-Until 'native picker cancellation' { $null -eq (Find-FolderDialog) }
    $field = Await-Control 'Project directory' ([System.Windows.Automation.ControlType]::Edit)
    if ($field.GetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern).Current.Value -cne $previous) { throw 'native picker cancellation changed the entered directory' }
    Invoke-Button 'Choose project location'
    Wait-Until 'native folder chooser reopened' { $null -ne (Find-FolderDialog) }
    $dialog = Find-FolderDialog
    Set-DialogLocation $dialog $projectRoot
    Invoke-DialogButton $dialog '1'
    Wait-Until 'selected parent returned to creation' {
        $null -eq (Find-FolderDialog) -and [string]::Equals(
            $field.GetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern).Current.Value,
            $projectDirectory, [StringComparison]::OrdinalIgnoreCase)
    }
    Save-Observation 'native-directory-picker-selected'
}
# Assert-ProjectURLClipboard records only an exact match to the ready project's published URL.
function Assert-ProjectURLClipboard {
    $selected = Get-Project
    $resources = @($selected.resources | Where-Object { $_.kind -ceq 'application' })
    if ($selected.state -cne 'ready' -or $resources.Count -ne 1) { throw 'copy requires one ready published application URL' }
    if ([Threading.Thread]::CurrentThread.GetApartmentState() -ne [Threading.ApartmentState]::STA) { throw 'native clipboard observation requires the admitted STA controller' }
    [System.Windows.Forms.Clipboard]::SetText('Harbor clipboard probe cleared')
    Invoke-Button 'Copy project URL'
    Wait-Until 'native clipboard receives the published project URL' { [System.Windows.Forms.Clipboard]::GetText() -ceq $resources[0].url }
    $null = Await-Control 'Project URL copied' ([System.Windows.Automation.ControlType]::Button)
    @{ project_id=$selected.id; expected_url=$resources[0].url; native_clipboard_exact_match=$true; copied_feedback_visible=$true } |
        ConvertTo-Json | Set-Content (Join-Path $EvidenceDirectory 'project-url-clipboard.json') -Encoding UTF8
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
function Invoke-PreauthorizedNetworking([string]$Action) {
    if ($Action -notin @('setup', 'release')) { throw 'fixture networking action is not allowed' }
    $tool = $env:HARBOR_PACKAGE_UI_WINDOWS_APPROVAL_TOOL
    if (-not $tool -or -not (Test-Path -LiteralPath $tool -PathType Leaf)) { throw 'preauthorized networking fixture is missing' }
    if ($null -eq $daemonProcess -or $daemonProcess.HasExited) { throw 'exact installed daemon is unavailable for fixture approval' }
    $birth = $daemonProcess.StartTime.ToFileTimeUtc().ToString()
    $path = Join-Path $EvidenceDirectory ("preauthorized-networking-" + $Action + '.json')
    $arguments = @('--action', $Action, '--installation-id', $selection.installation_id, '--daemon-pid', [string]$daemonProcess.Id, '--daemon-birth', $birth)
    $script:approvalIndeterminate = $true
    $process = Start-Process -FilePath $tool -ArgumentList $arguments -PassThru -NoNewWindow -RedirectStandardOutput $path -RedirectStandardError ($path + '.stderr')
    try {
        $null = $process.Handle
        if (-not $process.WaitForExit(190000)) {
            $process.Kill()
            if (-not $process.WaitForExit(5000)) { throw 'owned preauthorized fixture process did not settle after its deadline' }
            throw 'preauthorized networking fixture exceeded its native deadline; elevated helper quiescence is unproved'
        }
        $exit = $process.ExitCode
    } finally { $process.Dispose() }
    $output = Get-Content -LiteralPath $path -Raw
    if ($exit -ne 0) { throw "preauthorized exact-operation networking $Action failed: $output" }
    $receipt = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
    if ($receipt.profile -cne 'preauthorized-hosted-functional' -or $receipt.gui_consent_proved -ne $false -or
        $receipt.complete -ne $true -or $receipt.selection.installation_id -cne $selection.installation_id -or
        $receipt.selection.daemon_pid -ne $daemonProcess.Id -or [string]$receipt.selection.daemon_birth -cne $birth -or
        $receipt.user_sid -cne $selection.user_sid -or $receipt.selection.action -cne $Action) { throw 'networking fixture receipt crossed selected process or authorization scope' }
    $script:approvalIndeterminate = $false
}
$script:nativeQuitReceipts = @{}
function Invoke-NativeUIQuit([int]$ProductProcessID, [ValidateSet('first','final')][string]$Phase) {
    [HarborUIProfile]::ActivateRetainedProductWindow($ProductProcessID, $script:windowHandle)
    Wait-Until 'exact foreground installed window' { [HarborUIProfile]::IsRetainedProductWindowForeground($ProductProcessID, $script:windowHandle) } 10
    $quitFocus = Find-Control 'Projects' ([System.Windows.Automation.ControlType]::Button)
    if ($null -eq $quitFocus -or -not $quitFocus.Current.IsKeyboardFocusable) { throw 'installed UI has no keyboard-focusable Projects control for the native Quit shortcut' }
    $quitFocus.SetFocus()
    Wait-Until 'Quit shortcut target keyboard focus' { $quitFocus.Current.HasKeyboardFocus } 10
    if (-not [HarborUIProfile]::IsRetainedProductWindowForeground($ProductProcessID, $script:windowHandle)) { throw 'Quit keyboard target is not in the exact foreground product window' }
    $receipt = [HarborUIProfile]::SendRetainedProductQuit($ProductProcessID, $script:windowHandle)
    $script:nativeQuitReceipts[$Phase] = $receipt
    $receipt | ConvertTo-Json | Set-Content (Join-Path $EvidenceDirectory ('native-quit-' + $Phase + '-input.json')) -Encoding UTF8
    Wait-Until 'UI-only quit exits desktop' { [HarborUIProfile]::HasRetainedProductExited($ProductProcessID) }
}
function Open-Harbor {
    $launched = [HarborUIProfile]::Launch($launcherPath, $InstallRoot)
    Wait-Until 'one installed desktop process' { @(Get-Process -Name harbor-desktop -ErrorAction SilentlyContinue).Count -eq 1 } 60
    $process = Get-Process -Name harbor-desktop
    if ($process.Path -cne $desktopPath) { throw 'desktop process is outside admitted installation' }
    [HarborUIProfile]::RetainProductProcess($process.Id, $desktopPath)
    Wait-Until 'visible installed window' {
        $process.Refresh()
        if ($process.MainWindowHandle -eq [IntPtr]::Zero) { return $false }
        $script:window = [System.Windows.Automation.AutomationElement]::FromHandle($process.MainWindowHandle)
        return $null -ne $script:window -and -not $script:window.Current.IsOffscreen
    } 60
    $script:windowHandle = $process.MainWindowHandle
    if (-not [HarborUIProfile]::IsRetainedProductWindowVisible($process.Id, $script:windowHandle)) { throw 'reopened installed window is not natively visible' }
    return $process.Id
}
# Read-FixtureActivity uses the admitted daemon protocol without requesting any helper or lifecycle mutation.
function Read-FixtureActivity($Project, [string]$Name) {
    if ($null -eq $daemonProcess -or $daemonProcess.HasExited) { throw 'retained daemon is unavailable for activity observation' }
    $path = Join-Path $EvidenceDirectory ($Name + '-activity.json')
    $arguments = @('--action','observe','--installation-id',$selection.installation_id,'--daemon-pid',[string]$daemonProcess.Id,
        '--daemon-birth',$daemonProcess.StartTime.ToFileTimeUtc().ToString(),'--project-id',$Project.id,'--project-directory',$projectDirectory)
    $process = Start-Process -FilePath $env:HARBOR_PACKAGE_UI_WINDOWS_APPROVAL_TOOL -ArgumentList $arguments -PassThru -NoNewWindow -RedirectStandardOutput $path -RedirectStandardError ($path + '.stderr')
    try {
        $null = $process.Handle
        if (-not $process.WaitForExit(10000)) {
            $process.Kill()
            if (-not $process.WaitForExit(5000)) { throw 'read-only observer did not settle' }
            throw 'read-only activity observation exceeded its deadline'
        }
        if ($process.ExitCode -ne 0) { throw 'read-only activity observer refused the selected fixture' }
    } finally { $process.Dispose() }
    $receipt = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
    if ($receipt.complete -ne $true -or $receipt.selection.action -cne 'observe' -or $receipt.selection.installation_id -cne $selection.installation_id -or
        $receipt.selection.daemon_pid -ne $daemonProcess.Id -or [string]$receipt.selection.daemon_birth -cne $daemonProcess.StartTime.ToFileTimeUtc().ToString() -or
        $receipt.user_sid -cne $selection.user_sid -or $receipt.observation.project.id -cne $Project.id -or
        -not [string]::Equals($receipt.observation.project.path, $projectDirectory, [StringComparison]::OrdinalIgnoreCase)) { throw 'activity receipt differs from the selected daemon or fixture' }
    return $receipt.observation
}
$script:startupCaptureSequence = 0
$script:startupAfter = $null

# Save-StartupDiagnostics retains bounded public fixture output before timeout teardown retires the session.
function Save-StartupDiagnostics([string]$Phase) {
    $Name = 'startup-' + $script:startupCaptureSequence.ToString('D3') + '-' + $Phase
    $script:startupCaptureSequence++
    try {
        if ($null -ne $script:startupAfter) {
            $logs = Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)) 'npm-cache\_logs'
            & python (Join-Path $PSScriptRoot '..\..\..\scripts\packageui\windows\capture_npm.py') --fixture $projectDirectory --logs $logs --evidence $EvidenceDirectory --after $script:startupAfter.ToString('o')
            if ($LASTEXITCODE -ne 0) { throw 'bounded npm capture failed' }
        }
    } catch { Write-Warning "npm timing diagnostics omitted: $_" }
    try { Save-Observation $Name } catch { Write-Warning "startup observation failed: $_" }
    try { $project = Get-Project; if ($null -ne $project) { $null = Read-FixtureActivity $project $Name } } catch { Write-Warning "startup activity diagnostics failed: $_" }
    try {
        $trace = $projectDirectory
        foreach ($segment in @('_data', 'harbor', 'forj-dev.log')) {
            $item = Get-Item -LiteralPath $trace -Force
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'startup trace has a reparse ancestor' }
            $trace = Join-Path $trace $segment
        }
        if (Test-Path -LiteralPath $trace -PathType Leaf) {
            $item = Get-Item -LiteralPath $trace -Force
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'startup trace is a reparse point' }
            $stream = [IO.File]::Open($trace, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::ReadWrite)
            try {
                $null = $stream.Seek([Math]::Max(0, $stream.Length - 262144), [IO.SeekOrigin]::Begin)
                $bytes = New-Object byte[] 262144
                $count = $stream.Read($bytes, 0, $bytes.Length)
                [IO.File]::WriteAllText((Join-Path $EvidenceDirectory ($Name + '-forj-dev.log')), [Text.Encoding]::UTF8.GetString($bytes, 0, $count))
            } finally { $stream.Dispose() }
        }
    } catch { Write-Warning "startup trace diagnostics failed: $_" }
    try {
        if ($null -eq $daemonProcess -or $daemonProcess.HasExited) { throw 'retained daemon exited before process sampling' }
        $rows = @(Get-CimInstance Win32_Process -OperationTimeoutSec 5)
        $byID = @{}
        foreach ($row in $rows) { $byID[[int]$row.ProcessId] = $row }
        $facts = @()
        foreach ($row in $rows) {
            if ($facts.Count -ge 128) { break }
            $candidate = $row
            $reachesDaemon = $false
            for ($depth=0; $depth -lt 16; $depth++) {
                if ([int]$candidate.ProcessId -eq $daemonProcess.Id) { $reachesDaemon = $true; break }
                if (-not $byID.ContainsKey([int]$candidate.ParentProcessId)) { break }
                $candidate = $byID[[int]$candidate.ParentProcessId]
            }
            if (-not $reachesDaemon -or [int]$row.ProcessId -eq $daemonProcess.Id) { continue }
            # Snapshot ancestry only selects candidates; retained native handles and births below establish ownership.
            $held = [Collections.Generic.List[Diagnostics.Process]]::new()
            try {
                $current = $row
                $chain = @()
                $childBirth = [long]::MaxValue
                for ($depth=0; $depth -lt 16; $depth++) {
                    $process = Get-Process -Id $current.ProcessId -ErrorAction Stop
                    $null = $process.Handle
                    $held.Add($process)
                    $birth = $process.StartTime.ToFileTimeUtc()
                    if ($process.HasExited -or $process.SessionId -ne $daemonProcess.SessionId -or $birth -gt $childBirth -or
                        [Math]::Abs($birth - $current.CreationDate.ToFileTimeUtc()) -ge 10) { throw 'process sample identity or ancestry changed' }
                    $chain += [ordered]@{ pid=$process.Id; parent_pid=[int]$current.ParentProcessId; native_birth=$birth.ToString(); session=$process.SessionId; image=$process.Path }
                    if ($process.Id -eq $daemonProcess.Id) {
                        if ($birth -ne $daemonProcess.StartTime.ToFileTimeUtc()) { throw 'daemon ancestry birth changed' }
                        foreach ($retained in $held) { if ($retained.HasExited) { throw 'sampled descendant exited during observation' } }
                        $facts += [ordered]@{ native_identity_revalidated=$true; ancestry_source='bounded CIM snapshot correlated with retained native handles and births'; chain=$chain }
                        break
                    }
                    $childBirth = $birth
                    if (-not $byID.ContainsKey([int]$current.ParentProcessId)) { break }
                    $current = $byID[[int]$current.ParentProcessId]
                }
            } catch { Write-Warning "startup process sample omitted: $_" } finally { foreach ($retained in $held) { $retained.Dispose() } }
        }
        ConvertTo-Json -InputObject @($facts) -Depth 8 | Set-Content (Join-Path $EvidenceDirectory ($Name + '-processes.json')) -Encoding UTF8
    } catch { Write-Warning "startup process diagnostics failed: $_" }
}
function Await-ProjectState([string]$Expected) {
    $diagnostics = @{ next_capture=[DateTime]::MinValue }
    Wait-Until "project state $Expected" {
        $project = Get-Project
        if ($Expected -ceq 'ready' -and [DateTime]::UtcNow -ge $diagnostics.next_capture) {
            Save-StartupDiagnostics 'sample'
            $diagnostics.next_capture = [DateTime]::UtcNow.AddSeconds(15)
        }
        if ($null -eq $project) { return $false }
        if ($project.state -in @('failed', 'quarantined')) {
            if ($Expected -ceq 'ready') { Save-StartupDiagnostics 'terminal' }
            throw "project entered $($project.state)"
        }
        return $project.state -ceq $Expected
    } $(if ($Expected -ceq 'ready') { 360 } else { 240 })
}
function Unregister-Fixture($Project) {
    if (-not [string]::Equals($Project.path, $projectDirectory, [StringComparison]::OrdinalIgnoreCase)) { throw 'refusing removal outside the exact created fixture' }
    if (-not (Test-Path -LiteralPath $env:HARBOR_PACKAGE_UI_APPROVAL_TOOL -PathType Leaf)) { throw 'fixture approval tool is missing' }
    $intent = 'intent-windows-ui-remove-' + $Project.id
    $deadline = [DateTime]::UtcNow.AddSeconds(120)
    do {
        $output = & $cliPath remove $Project.id --intent $intent --json
        if ($LASTEXITCODE -ne 0) { throw 'fixture unregister did not return authoritative state' }
        $result = $output | ConvertFrom-Json
        $operation = $result.operation
        if ($operation.state -ceq 'succeeded') { return }
        if ($operation.state -in @('failed','cancelled')) { throw "fixture unregister ended $($operation.state)" }
        if ($operation.state -ceq 'requires_approval') {
            & $env:HARBOR_PACKAGE_UI_APPROVAL_TOOL --project-id $Project.id --operation-id $operation.id `
                --intent-id $intent --revision $result.revision --project-directory $projectDirectory
            if ($LASTEXITCODE -ne 0) { throw 'exact fixture removal approval failed' }
        }
        Start-Sleep -Milliseconds 250
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'fixture unregister exceeded its cleanup deadline'
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
[HarborUIProfile]::ProtectedDirectories = [string[]]@($InstallRoot, (Join-Path $InstallRoot $releaseRelative),
    (Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::CommonApplicationData)) 'GoForj\Harbor\Privileged'))
[HarborUIProfile]::ProtectedFiles = [string[]]@($launcherPath, $desktopPath, $daemonPath, $cliPath,
    (Join-Path $InstallRoot 'harbor-helper.exe'))
[HarborUIProfile]::ProtectedRegistryKey = 'Software\Microsoft\Windows\CurrentVersion\Uninstall\GoForjHarbor'
Assert-InteractiveWorker
try {
    $originalProcess = Open-Harbor
    $null = Get-Snapshot
    Wait-Until 'medium-integrity installed daemon' { @(Get-Process -Name harbord -ErrorAction SilentlyContinue).Count -eq 1 } 60
    $initialDaemons = @(Get-Process -Name harbord -ErrorAction SilentlyContinue)
    if ($initialDaemons.Count -ne 1) { throw 'initial installed daemon selection is ambiguous' }
    $daemonProcess = $initialDaemons[0]
    $null = $daemonProcess.Handle
    [HarborUIProfile]::AssertProductProcess($daemonProcess.Id)
    if ($daemonProcess.HasExited -or $daemonProcess.Path -cne $daemonPath) { throw 'initial daemon is unavailable or outside admitted installation' }
    [ordered]@{
        phase='initial-selection-after-authoritative-snapshot'; pid=$daemonProcess.Id
        native_birth=$daemonProcess.StartTime.ToFileTimeUtc().ToString(); image=$daemonProcess.Path
        session=$daemonProcess.SessionId; token_profile_verified=$true
    } | ConvertTo-Json | Set-Content (Join-Path $EvidenceDirectory 'initial-daemon-selection.json') -Encoding UTF8
    $null = Await-Control 'Dismiss getting started guide' ([System.Windows.Automation.ControlType]::Button)
    Save-Observation 'installed-first-launch'
    Wait-Until 'fresh setup action' {
        $null -ne (Find-Control 'Set up secure networking' ([System.Windows.Automation.ControlType]::Button))
    } 30
    Invoke-PreauthorizedNetworking 'setup'
    Wait-Until 'secure networking configured' { (Get-Snapshot).network_setup.configured -eq $true } 120
    Invoke-Button 'New project'
    Wait-Until 'creation form or prerequisite guidance' {
        $null -ne (Find-Control 'Project name' ([System.Windows.Automation.ControlType]::Edit)) -or
        $null -ne (Find-Control 'Recheck' ([System.Windows.Automation.ControlType]::Button))
    }
    if ($null -eq (Find-Control 'Project name' ([System.Windows.Automation.ControlType]::Edit))) {
        Save-Observation 'creation-prerequisites-before-recheck'
        # Exercise the offered repair action after preserving the first cold-tool observation.
        Invoke-Button 'Recheck'
    }
    Set-Field 'Project name' 'Project'
    Select-ProjectLocation
    Set-Field 'Project name' 'Harbor Installed Windows UI'
    Set-Field 'Go module' 'example.com/harbor-installed-windows-ui'
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
    $null = Await-Control 'Dismiss getting started guide' ([System.Windows.Automation.ControlType]::Button)
    $script:startupAfter = [DateTime]::UtcNow
    Invoke-Button 'Start project'
    Await-ProjectState 'ready'
    Assert-HTTPS
    Invoke-Button 'Recheck'
    $null = Await-Control 'Required tools are ready' ([System.Windows.Automation.ControlType]::Text)
    Invoke-Button 'Open app'
    $null = Await-Control '4 of 4 steps complete' ([System.Windows.Automation.ControlType]::Text)
    Save-Observation 'guide-complete'
    Invoke-Button 'Finish setup'
    Wait-Until 'guide dismissed after completion' { $null -eq (Find-Control 'Dismiss getting started guide' ([System.Windows.Automation.ControlType]::Button)) }
    Save-Observation 'project-ready'
    $originalWindowHandle = $script:windowHandle
    if (-not [HarborUIProfile]::IsRetainedProductWindowVisible($originalProcess, $originalWindowHandle)) { throw 'original installed window is not natively visible before close' }
    $script:window.GetCurrentPattern([System.Windows.Automation.WindowPattern]::Pattern).Close()
    Wait-Until 'close hides the installed window' { -not [HarborUIProfile]::IsRetainedProductWindowVisible($originalProcess, $originalWindowHandle) }
    @{ pid=$originalProcess; native_window=$originalWindowHandle.ToInt64().ToString(); process_alive=$true; native_visible_before=$true; native_visible_after=$false } |
        ConvertTo-Json | Set-Content (Join-Path $EvidenceDirectory 'native-window-close.json') -Encoding UTF8
    if ((Get-Project).state -cne 'ready') { throw 'closing the UI stopped the project' }
    if ((Open-Harbor) -ne $originalProcess) { throw 'reopening did not restore the original desktop process' }
    Invoke-NativeUIQuit $originalProcess 'first'
    if ((Get-Project).state -cne 'ready') { throw 'UI-only quit stopped the project' }
    @{ pid=$originalProcess; native_window=$script:windowHandle.ToInt64().ToString(); foreground_before=$true; focus_target='Projects'; user_key_chord='Ctrl+Q'; native_input=$script:nativeQuitReceipts['first']; process_exited=$true; project_state='ready' } |
        ConvertTo-Json | Set-Content (Join-Path $EvidenceDirectory 'native-window-quit.json') -Encoding UTF8
    $reopenedProcess = Open-Harbor
    Await-ProjectState 'ready'
    Assert-HTTPS
    Save-Observation 'reopened-running-project'
    $recoveryTool = $env:HARBOR_PACKAGE_UI_CRASH_RECOVERY_TOOL
    if (-not $recoveryTool -or -not (Test-Path -LiteralPath $recoveryTool -PathType Leaf)) { throw 'installed crash recovery client is missing' }
    $recoveryControllerPath = $env:PATH
    try {
        $env:PATH = $ToolsPath
        & $recoveryTool --project-id (Get-Project).id --project-directory $projectDirectory --evidence (Join-Path $EvidenceDirectory 'crash-recovery.json')
        if ($LASTEXITCODE -ne 0) { throw 'installed crash recovery client failed' }
    } finally {
        $env:PATH = $recoveryControllerPath
        # The original retained process exited intentionally; refresh only the selected installed daemon for subsequent approval and cleanup.
        $replacement = @(Get-Process -Name harbord -ErrorAction SilentlyContinue)
        if ($replacement.Count -ne 1) { throw 'exactly one recovered installed daemon is required' }
        $null = $replacement[0].Handle
        [HarborUIProfile]::AssertProductProcess($replacement[0].Id)
        if ($replacement[0].HasExited -or $replacement[0].Path -cne $daemonPath) { throw 'recovered daemon is outside admitted installation' }
        if ($null -ne $daemonProcess) { $daemonProcess.Dispose() }
        $daemonProcess = $replacement[0]
    }
    Await-ProjectState 'ready'
    Assert-HTTPS
    Invoke-Button 'Projects'
    Invoke-Button 'Refresh Harbor state'
    if ($null -eq (Find-Control 'Stop project' ([System.Windows.Automation.ControlType]::Button))) {
        Invoke-Button 'Projects'
        $projectLink = $null
        Wait-Until 'retained project link' {
            $links = $script:window.FindAll([System.Windows.Automation.TreeScope]::Descendants,
                [System.Windows.Automation.PropertyCondition]::new([System.Windows.Automation.AutomationElement]::ControlTypeProperty, [System.Windows.Automation.ControlType]::Hyperlink))
            $matches = @(for ($index=0; $index -lt $links.Count; $index++) {
                $link = $links.Item($index)
                if (([string]$link.Current.Name).StartsWith('Harbor Installed Windows UI', [StringComparison]::Ordinal)) { $link }
            })
            if ($matches.Count -gt 1) { throw 'multiple project links match the exact created project' }
            if ($matches.Count -eq 1) { $script:projectLink = $matches[0]; return $true }
            return $false
        }
        $script:projectLink.GetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern).Invoke()
    }
    $null = Await-Control 'Stop project' ([System.Windows.Automation.ControlType]::Button)
    Save-Observation 'reconnected-after-crash-recovery'
    Assert-ProjectURLClipboard
    Invoke-Button 'Stop project'
    Await-ProjectState 'stopped'
    Invoke-NativeUIQuit $reopenedProcess 'final'
    [ordered]@{
        native_quit_first=$script:nativeQuitReceipts['first']; native_quit_final=$script:nativeQuitReceipts['final']
        application=$desktopPath; launcher=$launcherPath; daemon=$daemonPath; bundle_digest=$selection.bundle_digest; project_directory=$projectDirectory; project_id=(Get-Project).id
        creation='production graphical wizard through native UIAutomation'; start='ready and system-trusted HTTPS 200'
        directory_picker='native cancellation preserves choices; selected parent produces the registered destination'
        clipboard='native clipboard exactly matches the daemon-published application URL'
        onboarding='installed first-project guide, native tool recheck, and published App opening completed'
        window_close='project remains ready'; second_launch='original process restored'
        crash_recovery='installed authenticated daemon crashes, safe retry, healthy peer and live GUI reconnect'; ui_quit='daemon and project remain ready'; ui_reopen='registration retained and HTTPS 200'; final_project_state='stopped'
    } | ConvertTo-Json | Set-Content (Join-Path $EvidenceDirectory 'installed-ui.json') -Encoding utf8
} catch {
    $originalFailure = $_
    try {
        ($originalFailure.Exception.ToString() + "`n" + $originalFailure.ScriptStackTrace) |
            Set-Content (Join-Path $EvidenceDirectory 'failure-exception.txt') -Encoding utf8
    } catch { Write-Warning "exception diagnostics failed: $_" }
    Save-Observation 'failure'
    try { [HarborUIProfile]::QuiesceLaunchedUIs() } catch { Write-Warning "failed to quiesce exact launched UI handles: $_" }
    throw $originalFailure
} finally {
    $cleanup = [ordered]@{ launched_token_facts=[HarborUIProfile]::LastLaunchFacts; gui_quiescent=$false; approval_indeterminate=$script:approvalIndeterminate; fixtures_stopped=$false; fixture_settlement_method='unproved'; fixture_source_state=$null; fixture_unregistered=$false; network_released=$false; daemon_absent=$false; retained_files=@{}; missing_files=@() }
    try {
        [HarborUIProfile]::QuiesceLaunchedUIs()
        $cleanup.gui_quiescent = $true
        if ($script:approvalIndeterminate) { throw 'approval is indeterminate; elevated helper quiescence is unproved, so machine cleanup and uninstall are blocked until disposable worker retirement' }
        $remaining = @(Get-Process -Name harbor-desktop -ErrorAction SilentlyContinue)
        if ($remaining.Count -ne 0) { throw 'an unrecorded desktop process prevents host cleanup' }
        $registered = Get-Project
        if ($null -ne $registered) {
            $cleanup.fixture_source_state = $registered.state
            if ($registered.state -cin @('quarantined', 'unavailable')) { throw 'quarantined fixture cannot be settled by GUI cleanup' }
            if ($registered.state -ceq 'failed') {
                $settled = Read-FixtureActivity $registered 'cleanup-failed-settlement'
                if ($settled.settled_failed -ne $true -or $settled.project.state -cne 'failed' -or $null -ne $settled.activity.session -or
                    $settled.activity.project_id -cne $registered.id -or $settled.failed_start.project_id -cne $registered.id -or
                    $settled.failed_start.kind -cne 'project.start' -or $settled.failed_start.state -cne 'failed') { throw 'failed fixture has no exact terminal failed-start and absent-session proof' }
            }
        }
        if ($null -ne $registered -and $registered.state -cnotin @('stopped', 'failed')) {
            & $cliPath stop $registered.id 1> (Join-Path $EvidenceDirectory 'cleanup-stop-stdout.txt') 2> (Join-Path $EvidenceDirectory 'cleanup-stop-stderr.txt')
            $stopExit = $LASTEXITCODE
            try { Get-Snapshot | ConvertTo-Json -Depth 30 | Set-Content (Join-Path $EvidenceDirectory 'cleanup-stop-snapshot.json') -Encoding UTF8 } catch { Write-Warning "stop-state diagnostics failed: $_" }
            if ($stopExit -ne 0) { throw 'fixture stop was not admitted' }
            Wait-Until 'stopped cleanup fixture' { (Get-Project).state -ceq 'stopped' } 120
        }
        if ($null -ne $registered) {
            # Failed startup can have no active session, so production unregister must prove settlement instead of inventing Stopped.
            Unregister-Fixture $registered
            if ($null -ne (Get-Project)) { throw 'removed fixture registration remains' }
            $cleanup.fixture_settlement_method = if ($registered.state -ceq 'failed') { 'terminal-failed-authoritative-unregister' } else { 'stopped-authoritative-unregister' }
        } else {
            $cleanup.fixture_settlement_method = 'authoritative-registration-absent'
        }
        # This compatibility field means no active fixture only after authoritative registration removal succeeds.
        $cleanup.fixtures_stopped = $true
        $cleanup.fixture_unregistered = $true
        Invoke-PreauthorizedNetworking 'release'
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
    $cleanup.approval_indeterminate = $script:approvalIndeterminate
    $cleanup | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $EvidenceDirectory 'cleanup.json') -Encoding utf8
    [HarborUIProfile]::CloseLaunchReceipts()
}
