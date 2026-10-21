param(
    [Parameter(Mandatory=$true)][string]$InstallRoot,
    [Parameter(Mandatory=$true)][string]$EvidenceDirectory
)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes, System.Windows.Forms
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

. (Join-Path $PSScriptRoot 'WorkerProfile.ps1')
[HarborUIProfile]::ProtectedDirectories = [string[]]@($InstallRoot, (Join-Path $InstallRoot $releaseRelative),
    (Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::CommonApplicationData)) 'GoForj\Harbor\Privileged'))
[HarborUIProfile]::ProtectedFiles = [string[]]@($launcherPath, $desktopPath, $daemonPath, $cliPath,
    (Join-Path $InstallRoot 'harbor-helper.exe'))
[HarborUIProfile]::ProtectedRegistryKey = 'Software\Microsoft\Windows\CurrentVersion\Uninstall\GoForjHarbor'
Assert-InteractiveWorker
$daemonProcess = $null
$daemonAdmitted = $false
$script:window = $null
$script:windowHandle = [IntPtr]::Zero
$script:untouchedSnapshotVerified = $false

# Wait-Until bounds native observations without turning a missing prerequisite into a pass.
function Wait-Until([string]$Description, [scriptblock]$Condition, [int]$Seconds=30) {
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    do {
        if (& $Condition) { return }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Timed out: $Description"
}
# Assert-UntouchedSnapshot prevents a window probe from granting or concealing machine authority.
function Assert-UntouchedSnapshot([string]$Name) {
    $output = & $cliPath daemon snapshot
    if ($LASTEXITCODE -ne 0) { throw 'installed CLI snapshot failed' }
    $snapshot = $output | ConvertFrom-Json
    $snapshot | ConvertTo-Json -Depth 30 | Set-Content (Join-Path $EvidenceDirectory ($Name + '-snapshot.json')) -Encoding UTF8
    foreach ($field in @('schema_version','projects','operations','network_setup')) {
        if ($field -notin $snapshot.PSObject.Properties.Name) { throw "window-only snapshot omits $field" }
    }
    if ($snapshot.schema_version -ne 1 -or $null -eq $snapshot.network_setup -or
        $snapshot.network_setup.configured -ne $false -or $null -ne $snapshot.network_setup.pending -or
        @($snapshot.projects).Count -ne 0 -or @($snapshot.operations).Count -ne 0 -or
        @($snapshot.recent_resource_ids).Count -ne 0 -or $snapshot.network_capacity -gt 0) {
        throw 'window-only probe requires unconfigured networking, no pending authority, no projects and no prior operations'
    }
    if (@(Get-Process -Name harbor-helper, consent -ErrorAction SilentlyContinue).Count -ne 0) {
        throw 'helper or native consent process prevents proving absent pending authority'
    }
    $script:untouchedSnapshotVerified = $true
}
# Find-Control scopes automation to the exact admitted installed native window.
function Find-Control([string]$Name, [System.Windows.Automation.ControlType]$Type) {
    $conditions = [System.Windows.Automation.Condition[]]@(
        [System.Windows.Automation.PropertyCondition]::new([System.Windows.Automation.AutomationElement]::NameProperty, $Name),
        [System.Windows.Automation.PropertyCondition]::new([System.Windows.Automation.AutomationElement]::ControlTypeProperty, $Type)
    )
    return $script:window.FindFirst([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.AndCondition]::new($conditions))
}
# Open-Harbor admits each actual installed UI token and retains its process handle before interaction.
function Open-Harbor {
    $launched = [HarborUIProfile]::Launch($launcherPath, $InstallRoot)
    Wait-Until 'one installed desktop process' { @(Get-Process -Name harbor-desktop -ErrorAction SilentlyContinue).Count -eq 1 } 60
    $process = Get-Process -Name harbor-desktop
    [HarborUIProfile]::RetainProductProcess($process.Id, $desktopPath)
    Wait-Until 'visible installed native window' {
        $process.Refresh()
        if ($process.MainWindowHandle -eq [IntPtr]::Zero) { return $false }
        $script:window = [System.Windows.Automation.AutomationElement]::FromHandle($process.MainWindowHandle)
        return $null -ne $script:window -and -not $script:window.Current.IsOffscreen
    } 60
    $script:windowHandle = $process.MainWindowHandle
    if (-not [HarborUIProfile]::IsRetainedProductWindowVisible($process.Id, $script:windowHandle)) { throw 'installed window is not natively visible' }
    return $process.Id
}
# Save-WindowControls retains UIAutomation evidence without calling a product API.
function Save-WindowControls([string]$Name) {
    if ($null -eq $script:window) { return }
    $controls = $script:window.FindAll([System.Windows.Automation.TreeScope]::Descendants, [System.Windows.Automation.Condition]::TrueCondition)
    $facts = for ($index=0; $index -lt [Math]::Min($controls.Count, 3000); $index++) {
        $current = $controls.Item($index).Current
        [ordered]@{ name=$current.Name; type=$current.ControlType.ProgrammaticName; enabled=$current.IsEnabled; offscreen=$current.IsOffscreen; keyboard_focusable=$current.IsKeyboardFocusable }
    }
    $facts | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $EvidenceDirectory ($Name + '-controls.json')) -Encoding UTF8
}
try {
    if (@(Get-Process -Name harbor-desktop, harbord, harbor-helper -ErrorAction SilentlyContinue).Count -ne 0) { throw 'preexisting product process prevents fresh installed window proof' }
    $originalProcess = Open-Harbor
    Wait-Until 'installed daemon started by installed UI' { @(Get-Process -Name harbord -ErrorAction SilentlyContinue).Count -eq 1 } 60
    $daemonProcess = Get-Process -Name harbord
    $null = $daemonProcess.Handle
    if ($daemonProcess.Path -cne $daemonPath) { throw 'daemon image is outside the admitted installation' }
    [HarborUIProfile]::AssertProductProcess($daemonProcess.Id)
    $daemonAdmitted = $true
    $daemonBirth = $daemonProcess.StartTime.ToFileTimeUtc().ToString()
    Assert-UntouchedSnapshot 'first-launch'
    Save-WindowControls 'first-launch'
    $originalWindowHandle = $script:windowHandle
    $script:window.GetCurrentPattern([System.Windows.Automation.WindowPattern]::Pattern).Close()
    Wait-Until 'native close hides original installed window' { -not [HarborUIProfile]::IsRetainedProductWindowVisible($originalProcess, $originalWindowHandle) }
    if ([HarborUIProfile]::HasRetainedProductExited($originalProcess)) { throw 'native close exited the original desktop process' }
    if ((Open-Harbor) -ne $originalProcess -or $script:windowHandle -ne $originalWindowHandle) { throw 'reopen did not restore the original native window and process' }
    Assert-UntouchedSnapshot 'reopened'
    $nativeMenu = [HarborUIProfile]::RetainedProductMenu($originalProcess, $script:windowHandle)
    $nativeMenu | ConvertTo-Json | Set-Content (Join-Path $EvidenceDirectory 'native-menu.json') -Encoding UTF8
    if ('Harbor' -cnotin $nativeMenu -or @($nativeMenu | Where-Object { ($_ -split "`t")[0] -ceq 'Harbor/Quit Harbor UI' }).Count -ne 1) { throw 'native Harbor menu lacks the exact Quit action' }
    [HarborUIProfile]::ActivateRetainedProductWindow($originalProcess, $script:windowHandle)
    Wait-Until 'exact foreground installed window' { [HarborUIProfile]::IsRetainedProductWindowForeground($originalProcess, $script:windowHandle) } 10
    Wait-Until 'focusable Projects target' {
        $control = Find-Control 'Projects' ([System.Windows.Automation.ControlType]::Button)
        $null -ne $control -and $control.Current.IsEnabled -and $control.Current.IsKeyboardFocusable
    } 30
    $quitFocus = Find-Control 'Projects' ([System.Windows.Automation.ControlType]::Button)
    $quitFocus.SetFocus()
    Wait-Until 'Quit shortcut target keyboard focus' { $quitFocus.Current.HasKeyboardFocus } 10
    if (-not [HarborUIProfile]::IsRetainedProductWindowForeground($originalProcess, $script:windowHandle)) { throw 'Quit shortcut target is not in the exact foreground window' }
    Save-WindowControls 'before-native-quit'
    $nativeQuit = [HarborUIProfile]::SendRetainedProductQuit($originalProcess, $script:windowHandle)
    $nativeQuit | ConvertTo-Json | Set-Content (Join-Path $EvidenceDirectory 'native-quit-input.json') -Encoding UTF8
    Wait-Until 'normal native Quit exits exact retained UI handle' { [HarborUIProfile]::HasRetainedProductExited($originalProcess) }
    if ($daemonProcess.HasExited -or $daemonProcess.StartTime.ToFileTimeUtc().ToString() -cne $daemonBirth) { throw 'normal UI Quit stopped or replaced the original daemon' }
    Assert-UntouchedSnapshot 'after-native-quit'
    [ordered]@{
        schema_version=1; scope='installed-native-window-only'; full_gui_lifecycle_proved=$false
        application=$desktopPath; launcher=$launcherPath; daemon=$daemonPath; installation_id=$selection.installation_id; bundle_digest=$selection.bundle_digest
        ui_pid=$originalProcess; native_window=$originalWindowHandle.ToInt64().ToString(); native_close_hides=$true; original_process_reopen=$true
        native_application_menu='Harbor'; quit_action='Ctrl+Q'; native_quit_input=$nativeQuit; focus_target='Projects'; exact_retained_ui_handle_exited=$true
        original_daemon_survived=$true; daemon_pid=$daemonProcess.Id; daemon_native_birth=$daemonBirth
        networking_configured=$false; projects=0; prior_operations=0; setup_or_helper_actions_requested=$false
        launched_token_facts=[HarborUIProfile]::LastLaunchFacts; protected_access_receipts=@([HarborUIProfile]::AccessReceipts)
    } | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $EvidenceDirectory 'installed-window-only.json') -Encoding UTF8
} catch {
    $failure = $_
    try { ($failure.Exception.ToString() + "`n" + $failure.ScriptStackTrace) | Set-Content (Join-Path $EvidenceDirectory 'window-only-failure.txt') -Encoding UTF8 } catch { Write-Warning "failure receipt could not be saved: $_" }
    try { Save-WindowControls 'failure' } catch { Write-Warning "window diagnostics failed: $_" }
    throw $failure
} finally {
    $cleanup = [ordered]@{
        schema_version=1; scope='installed-native-window-only'; gui_quiescent=$false; daemon_absent=$false
        authority_absent=$false; approval_indeterminate=$true; untouched_snapshot_verified=$false
        retained_files=@{}; missing_files=@(); launched_token_facts=[HarborUIProfile]::LastLaunchFacts
        protected_access_receipts=@([HarborUIProfile]::AccessReceipts)
    }
    try {
        # Only retained launcher/UI handles may be retired, including after a failed native Quit.
        [HarborUIProfile]::QuiesceLaunchedUIs()
        if (@(Get-Process -Name harbor-desktop -ErrorAction SilentlyContinue).Count -ne 0) { throw 'unrecorded UI process prevents uninstall' }
        $cleanup.gui_quiescent = $true
        if (-not $daemonAdmitted -or $null -eq $daemonProcess -or $daemonProcess.HasExited) { throw 'original started daemon is unavailable for authoritative cleanup admission' }
        Assert-UntouchedSnapshot 'before-daemon-stop'
        $cleanup.authority_absent = $true
        $cleanup.approval_indeterminate = $false
        $cleanup.untouched_snapshot_verified = $script:untouchedSnapshotVerified
        & $cliPath daemon stop
        if ($LASTEXITCODE -ne 0) { throw 'installed daemon shutdown was not acknowledged' }
        Wait-Until 'exact original daemon handle exit' { $daemonProcess.HasExited }
        if (@(Get-Process -Name harbord, harbor-helper, consent -ErrorAction SilentlyContinue).Count -ne 0) { throw 'daemon or helper authority remains after shutdown' }
        $cleanup.daemon_absent = $true
        $database = Join-Path $env:LOCALAPPDATA 'GoForj\Harbor\harbor.db'
        $item = Get-Item -LiteralPath $database -Force
        if ($item.PSIsContainer -or $item.Length -le 0 -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'retained user database is not a nonempty regular file' }
        $cleanup.retained_files[$database] = (Get-FileHash -LiteralPath $database -Algorithm SHA256).Hash
    } catch { $cleanup.error = $_.ToString(); Write-Warning "partial window-only cleanup: $_" }
    $cleanup | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $EvidenceDirectory 'cleanup.json') -Encoding UTF8
    [HarborUIProfile]::CloseLaunchReceipts()
    if ($null -ne $daemonProcess) { $daemonProcess.Dispose() }
}
