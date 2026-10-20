param([Parameter(Mandatory=$true)][ValidateSet('register', 'start', 'inspect', 'remove')][string]$Action)
$ErrorActionPreference = 'Stop'
$PSModuleAutoLoadingPreference = 'None'
$env:PSModulePath = ''
Import-Module -Name ([IO.Path]::Combine([Environment]::SystemDirectory, 'WindowsPowerShell\v1.0\Modules\Microsoft.PowerShell.Utility\Microsoft.PowerShell.Utility.psd1'))

# New-OwnedTaskDefinition fixes every authored property before full scheduler XML admission.
function New-OwnedTaskDefinition($Service, [string]$Root, [string]$SID, [string]$Name, $Selection) {
    $definition = $Service.NewTask(0)
    $definition.RegistrationInfo.Author = 'com.goforj.harbor'
    $definition.RegistrationInfo.Description = "com.goforj.harbor installation $($selection.installation_id) user $sid"
    $definition.RegistrationInfo.Source = "com.goforj.harbor $($selection.bundle_digest)"
    $definition.RegistrationInfo.URI = "\$name"
    $definition.Principal.Id = 'HarborUser'
    $definition.Principal.UserId = $sid
    $definition.Principal.LogonType = 3
    $definition.Principal.RunLevel = 0
    $trigger = $definition.Triggers.Create(9)
    $trigger.Id = 'HarborLogon'
    $trigger.UserId = $sid
    $trigger.Enabled = $true
    $trigger.Delay = 'PT1S'
    $execution = $definition.Actions.Create(0)
    $execution.Id = 'HarborDaemon'
    $execution.Path = [IO.Path]::Combine($root, 'daemon-launcher.exe')
    $execution.Arguments = ''
    $execution.WorkingDirectory = $root
    $settings = $definition.Settings
    $settings.AllowDemandStart = $true
    $settings.AllowHardTerminate = $false
    $settings.Compatibility = 2
    $settings.DisallowStartIfOnBatteries = $false
    $settings.StopIfGoingOnBatteries = $false
    $settings.Enabled = $true
    $settings.ExecutionTimeLimit = 'PT0S'
    $settings.Hidden = $false
    $settings.MultipleInstances = 2
    $settings.Priority = 7
    $settings.RunOnlyIfIdle = $false
    $settings.RunOnlyIfNetworkAvailable = $false
    $settings.StartWhenAvailable = $false
    $settings.WakeToRun = $false
    $settings.IdleSettings.StopOnIdleEnd = $false
    $settings.IdleSettings.RestartOnIdle = $false
    $settings.IdleSettings.IdleDuration = 'PT10M'
    $settings.IdleSettings.WaitTimeout = 'PT1H'
    $settings.DisallowStartOnRemoteAppSession = $true
    $settings.UseUnifiedSchedulingEngine = $false
    
    return $definition
}

# Convert-TaskXML keeps the full scheduler definition authoritative, including fields Harbor did not author.
function Convert-TaskXML([string]$Text) {
    $document = [xml]::new()
    $document.PreserveWhitespace = $true
    $document.LoadXml($Text)
    foreach ($node in @($document.SelectNodes('//text()[normalize-space(.)=""]'))) {
        if ($node.ParentNode.SelectNodes('*').Count -gt 0) { $null = $node.ParentNode.RemoveChild($node) }
    }
    return $document.DocumentElement.OuterXml
}

# Convert-TaskACL compares binary descriptors rather than accepting alternate or additional access grants.
function Convert-TaskACL([string]$Text) {
    $descriptor = [Security.AccessControl.RawSecurityDescriptor]::new($Text)
    $bytes = [byte[]]::new($descriptor.BinaryLength)
    $descriptor.GetBinaryForm($bytes, 0)
    return [Convert]::ToBase64String($bytes)
}

# Get-OwnedTask distinguishes absence from scheduler observation errors and preserves every drifted task.
function Get-OwnedTask($Folder, [string]$Name, $Definition, [string]$SDDL) {
    try { $task = $folder.GetTask($name) } catch {
        $cause = $_.Exception
        while ($null -ne $cause.InnerException) { $cause = $cause.InnerException }
        if ($cause.HResult -eq -2147024894) { return $null }
        throw
    }
    if ((Convert-TaskXML $task.Definition.XmlText) -cne (Convert-TaskXML $definition.XmlText)) { throw 'preserve non-matching Harbor daemon task definition' }
    if ((Convert-TaskACL $task.GetSecurityDescriptor(7)) -cne (Convert-TaskACL $sddl)) { throw 'preserve non-matching Harbor daemon task security descriptor' }
    return $task
}

# Assert-InteractiveSession rejects a different administrator's elevation and headless service logons.
function Assert-InteractiveSession([string]$SID, [int]$Session, [bool]$Mutation, [string]$Installer) {
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class HarborTaskNative {
    [DllImport("user32.dll")] public static extern IntPtr GetShellWindow();
    [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr window, out uint process);
    [DllImport("advapi32.dll", SetLastError=true)] public static extern bool OpenProcessToken(IntPtr process, uint access, out IntPtr token);
    [DllImport("kernel32.dll")] public static extern bool CloseHandle(IntPtr handle);
    [StructLayout(LayoutKind.Sequential)] public struct BasicInformation {
        public IntPtr Reserved1, PebBaseAddress, Reserved2, Reserved3, ProcessId, ParentId;
    }
    [DllImport("ntdll.dll")] public static extern int NtQueryInformationProcess(IntPtr process, int information, out BasicInformation value, int size, out int length);
}
'@
    $window = [HarborTaskNative]::GetShellWindow()
    [uint32]$shellID = 0
    if ($window -eq [IntPtr]::Zero -or [HarborTaskNative]::GetWindowThreadProcessId($window, [ref]$shellID) -eq 0) { throw 'interactive Explorer shell is unavailable' }
    $shell = [Diagnostics.Process]::GetProcessById($shellID)
    try {
        $expected = [IO.Path]::Combine([Environment]::GetFolderPath([Environment+SpecialFolder]::Windows), 'explorer.exe')
        if ($shell.SessionId -ne $Session -or -not [string]::Equals($shell.MainModule.FileName, $expected, [StringComparison]::OrdinalIgnoreCase)) { throw 'interactive shell identity or session differs' }
        [IntPtr]$token = [IntPtr]::Zero
        if (-not [HarborTaskNative]::OpenProcessToken($shell.Handle, 8, [ref]$token)) { throw 'cannot inspect interactive shell user' }
        try {
            $identity = [Security.Principal.WindowsIdentity]::new($token)
            try { if ($identity.User.Value -cne $SID) { throw 'installer and interactive shell users differ' } } finally { $identity.Dispose() }
        } finally { $null = [HarborTaskNative]::CloseHandle($token) }
    } finally { $shell.Dispose() }
    if ($Mutation) {
        $basic = [HarborTaskNative+BasicInformation]::new()
        [int]$length = 0
        if ([HarborTaskNative]::NtQueryInformationProcess([Diagnostics.Process]::GetCurrentProcess().Handle, 0, [ref]$basic, [Runtime.InteropServices.Marshal]::SizeOf($basic), [ref]$length) -ne 0) { throw 'cannot inspect installer parent' }
        $parent = [Diagnostics.Process]::GetProcessById($basic.ParentId.ToInt32())
        try {
            if ($parent.SessionId -ne $Session -or -not [string]::Equals($parent.MainModule.FileName, $Installer, [StringComparison]::OrdinalIgnoreCase)) { throw 'task mutation requires the admitted installer parent' }
        } finally { $parent.Dispose() }
    }
}

# Enter-OwnedTaskLock serializes cooperating launchers across sessions and rejects precreated foreign objects.
function Enter-OwnedTaskLock([string]$SID) {
    $security = [Security.AccessControl.MutexSecurity]::new()
    $security.SetOwner([Security.Principal.SecurityIdentifier]::new($SID))
    $security.SetAccessRuleProtection($true, $false)
    $access = [Security.AccessControl.MutexRights]::Synchronize -bor [Security.AccessControl.MutexRights]::Modify -bor [Security.AccessControl.MutexRights]::ReadPermissions
    foreach ($principal in @('S-1-5-32-544', 'S-1-5-18', $SID)) {
        $rights = [Security.AccessControl.MutexRights]::FullControl
        if ($principal -ceq $SID) { $rights = $access }
        $security.AddAccessRule([Security.AccessControl.MutexAccessRule]::new([Security.Principal.SecurityIdentifier]::new($principal), $rights, [Security.AccessControl.AccessControlType]::Allow))
    }
    $name = "Global\GoForjHarborTask-$SID"
    $mutex = $null
    try {
        try { $mutex = [Threading.Mutex]::OpenExisting($name, $access) } catch [Threading.WaitHandleCannotBeOpenedException] {
            [bool]$created = $false
            $mutex = [Threading.Mutex]::new($false, $name, [ref]$created, $security)
        }
        $sections = [Security.AccessControl.AccessControlSections]::Owner -bor [Security.AccessControl.AccessControlSections]::Access
        $actual = $mutex.GetAccessControl().GetSecurityDescriptorSddlForm($sections)
        $expected = $security.GetSecurityDescriptorSddlForm($sections)
        if ((Convert-TaskACL $actual) -cne (Convert-TaskACL $expected)) { throw 'preserve foreign Harbor task synchronization object' }
        try {
            if (-not $mutex.WaitOne(30000)) { throw 'Harbor task operation is busy' }
        } catch [Threading.AbandonedMutexException] {
            $mutex.ReleaseMutex()
            throw 'previous Harbor task operation did not settle'
        }
        return $mutex
    } catch {
        if ($null -ne $mutex) { $mutex.Dispose() }
        throw
    }
}

# Production task policy starts here. Tests load only the functions above.
$root = [IO.Path]::Combine([Environment]::GetFolderPath([Environment+SpecialFolder]::ProgramFiles), 'GoForj\Harbor')
$selection = [IO.File]::ReadAllText([IO.Path]::Combine($root, 'installation.json')) | ConvertFrom-Json
if ($selection.schema_version -ne 1 -or $selection.product_id -cne 'com.goforj.harbor' -or $selection.installation_id -cnotmatch '^[a-f0-9]{32}$' -or $selection.bundle_digest -cnotmatch '^[a-f0-9]{64}$') { throw 'invalid admitted Windows installation identity' }
$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$session = [Diagnostics.Process]::GetCurrentProcess().SessionId
if ($sid -cne $selection.user_sid -or $sid -eq 'S-1-5-18' -or $session -le 0) { throw 'Harbor requires the intended interactive user session' }
if (($Action -eq 'register' -or $Action -eq 'remove') -and -not ([Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'task mutation requires the elevated same-user installer' }
Assert-InteractiveSession $sid $session ($Action -eq 'register' -or $Action -eq 'remove') ([IO.Path]::Combine($root, 'releases', [string]$selection.release_sequence, 'harbor-installer.exe'))
$name = "GoForjHarborDaemon-$sid"
$sddl = "O:BAG:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;GRGX;;;$sid)"
$service = [Activator]::CreateInstance([Type]::GetTypeFromProgID('Schedule.Service'))
$service.Connect()
$folder = $service.GetFolder('\')
$definition = New-OwnedTaskDefinition $service $root $sid $name $selection
$lock = Enter-OwnedTaskLock $sid
try {
$task = Get-OwnedTask $folder $name $definition $sddl
switch ($Action) {
    'register' {
        if ($null -eq $task) {
            $null = $folder.RegisterTaskDefinition($name, $definition, 18, $sid, $null, 3, $sddl)
            $task = Get-OwnedTask $folder $name $definition $sddl
            if ($null -eq $task) { throw 'registered Harbor task is absent' }
        }
    }
    'start' {
        if ($null -eq $task) { throw 'installed Harbor daemon task is missing; repair the same-user installation' }
        $null = $task.RunEx($null, 4 -bor 8, $session, $sid)
    }
    'inspect' {
        if ($null -eq $task) { throw 'installed Harbor daemon task is missing' }
    }
    'remove' {
        if ($null -ne $task) {
            if ($task.GetInstances(0).Count -ne 0 -or $task.State -eq 2 -or $task.State -eq 4) { throw 'Harbor task is live; stop the daemon and retry uninstall' }
            $task = Get-OwnedTask $folder $name $definition $sddl
            if ($null -eq $task -or $task.GetInstances(0).Count -ne 0 -or $task.State -eq 2 -or $task.State -eq 4) { throw 'Harbor task changed before removal' }
            $folder.DeleteTask($name, 0)
            if ($null -ne (Get-OwnedTask $folder $name $definition $sddl)) { throw 'Harbor task remains after removal' }
        }
    }
}
} finally {
    $lock.ReleaseMutex()
    $lock.Dispose()
}
Write-Output "Harbor task $Action admitted for $sid session $session"
