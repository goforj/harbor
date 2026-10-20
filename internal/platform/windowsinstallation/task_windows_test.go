//go:build windows

package windowsinstallation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTaskActionAdmission rejects attempts to select a different task or inject policy arguments.
func TestTaskActionAdmission(t *testing.T) {
	for _, action := range []string{"register", "start", "inspect", "remove"} {
		if err := admitTaskAction(action); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"", "Start", "update", "start -SID S-1-5-18", "remove; whoami"} {
		if err := admitTaskAction(action); err == nil {
			t.Fatalf("admitted action %q", action)
		}
	}
	if err := (&Installation{}).Task(context.Background(), "inspect"); err == nil {
		t.Fatal("closed installation admitted task execution")
	}
}

// TestTaskUserAdmission rejects cross-user, service, and noninteractive scheduler activation.
func TestTaskUserAdmission(t *testing.T) {
	user := "S-1-5-21-1-2-3-1001"
	if err := admitTaskUser(user, user, 1); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		selected, current string
		session           uint32
	}{
		{user, "S-1-5-21-1-2-3-1002", 1},
		{user, user, 0},
		{"", user, 1},
		{systemSID, systemSID, 1},
		{"S-1-5-32-544", "S-1-5-32-544", 1},
	} {
		if err := admitTaskUser(fixture.selected, fixture.current, fixture.session); err == nil {
			t.Fatalf("admitted task user fixture %+v", fixture)
		}
	}
}

// TestTaskEnvironment removes mixed-case module overrides without changing unrelated user environment.
func TestTaskEnvironment(t *testing.T) {
	source := []string{"Path=C:\\ordinary", "psmodulepath=C:\\foreign", "PSMODULEAUTOLOADINGPREFERENCE=All", "LOCALAPPDATA=C:\\profile"}
	got := taskEnvironment(source)
	expected := []string{source[0], source[3], "PSModulePath=", "PSModuleAutoLoadingPreference=None"}
	if strings.Join(got, "\n") != strings.Join(expected, "\n") || source[1] != "psmodulepath=C:\\foreign" {
		t.Fatalf("unexpected environment %q or modified input %q", got, source)
	}
}

// TestNativeTaskPolicyDefinition verifies COM's canonical full-definition serialization without registering a task.
func TestNativeTaskPolicyDefinition(t *testing.T) {
	runTaskPolicyFixture(t, `
$service = [Activator]::CreateInstance([Type]::GetTypeFromProgID('Schedule.Service'))
$service.Connect()
$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$selection = @{ installation_id = ('a' * 32); bundle_digest = ('b' * 64) }
$definition = New-OwnedTaskDefinition $service 'C:\HarborNativeFixture' $sid 'HarborNativeFixture' $selection
$copy = $service.NewTask(0)
$copy.XmlText = $definition.XmlText
if ((Convert-TaskXML $copy.XmlText) -cne (Convert-TaskXML $definition.XmlText)) { throw 'COM XML roundtrip drift' }
${principalSID} = $definition.Principal.UserId
if ($principalSID -notmatch '^S-1-') { $principalSID = ([Security.Principal.NTAccount]::new($principalSID)).Translate([Security.Principal.SecurityIdentifier]).Value }
if ($definition.Principal.LogonType -ne 3 -or $definition.Principal.RunLevel -ne 0 -or $principalSID -cne $sid) { throw 'unsafe principal' }
if ($definition.Actions.Count -ne 1 -or $definition.Actions.Item(1).Arguments -ne '' -or $definition.Actions.Item(1).Path -cne 'C:\HarborNativeFixture\daemon-launcher.exe') { throw 'unsafe action' }
if ($definition.Triggers.Count -ne 1 -or ([Security.Principal.NTAccount]::new($definition.Triggers.Item(1).UserId)).Translate([Security.Principal.SecurityIdentifier]).Value -cne $sid) { throw 'unsafe trigger' }
$before = Convert-TaskXML $copy.XmlText
$copy.Settings.DeleteExpiredTaskAfter = 'PT1H'
if ((Convert-TaskXML $copy.XmlText) -ceq $before) { throw 'unlisted setting drift escaped XML admission' }
$sddl = Get-OwnedTaskSDDL $sid
if ((Convert-TaskACL $sddl) -ceq (Convert-TaskACL ($sddl + '(A;;FA;;;BU)'))) { throw 'extra ACL grant admitted' }
if ((Convert-TaskXML '<Task><Arguments> </Arguments></Task>') -ceq (Convert-TaskXML '<Task><Arguments></Arguments></Task>')) { throw 'significant argument whitespace was discarded' }
if ((Convert-TaskXML ('<Task>' + [Environment]::NewLine + ' <Actions />' + [Environment]::NewLine + '</Task>')) -cne (Convert-TaskXML '<Task><Actions /></Task>')) { throw 'formatting whitespace was retained' }
Write-Output 'native task definition passed'
`)
}

// TestNativeTaskPolicySchedulerAdmission registers only a unique disposable CI task and proves exact reuse and drift refusal.
func TestNativeTaskPolicySchedulerAdmission(t *testing.T) {
	if os.Getenv("HARBOR_ADMISSION_FIXTURE_ROOT") == "" {
		t.Skip("disposable native CI fixture was not configured")
	}
	runTaskPolicyFixture(t, `
$service = [Activator]::CreateInstance([Type]::GetTypeFromProgID('Schedule.Service'))
$service.Connect()
$folder = $service.GetFolder('\')
$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$name = 'HarborNativeAdmission-' + [Guid]::NewGuid().ToString('N')
$selection = @{ installation_id = ('a' * 32); bundle_digest = ('b' * 64) }
$definition = New-OwnedTaskDefinition $service 'C:\HarborNativeFixture' $sid $name $selection
$sddl = Get-OwnedTaskSDDL $sid
if ($null -ne (Get-OwnedTask $folder $name $definition $sddl)) { throw 'fresh fixture task exists' }
$lock = Enter-OwnedTaskLock $sid
$lock.ReleaseMutex()
$lock.Dispose()
$created = $false
try {
    $registered = $folder.RegisterTaskDefinition($name, $definition, 18, $sid, $null, 3, $sddl)
    $createdXML = Convert-TaskXML $registered.Definition.XmlText
    $createdACL = Convert-TaskACL $registered.GetSecurityDescriptor(7)
    $created = $true
    try { $task = Get-OwnedTask $folder $name $definition $sddl } catch {
        [Console]::Error.WriteLine('expected fixture XML: ' + (Convert-TaskXML $definition.XmlText))
        [Console]::Error.WriteLine('registered fixture XML: ' + $createdXML)
        [Console]::Error.WriteLine('expected fixture ACL: ' + $sddl)
        [Console]::Error.WriteLine('registered fixture ACL: ' + $registered.GetSecurityDescriptor(7))
        throw
    }
    if ($null -eq $task) { throw 'exact registered task absent' }
    $originalXML = Convert-TaskXML $task.Definition.XmlText
    $originalACL = Convert-TaskACL $task.GetSecurityDescriptor(7)
    $mutations = @(
        { param($d) $d.Principal.RunLevel = 1 },
        { param($d) $d.Principal.LogonType = 2 },
        { param($d) $d.Actions.Item(1).Arguments = '--foreign' },
        { param($d) $d.Actions.Item(1).Path = 'C:\foreign.exe' },
        { param($d) $d.Actions.Item(1).WorkingDirectory = 'C:\foreign' },
        { param($d) $d.Settings.Hidden = $true },
        { param($d) $d.Settings.DeleteExpiredTaskAfter = 'PT1H' },
        { param($d) $d.RegistrationInfo.Source = 'foreign' },
        { param($d) $d.RegistrationInfo.Description = 'foreign' },
        { param($d) $d.Triggers.Item(1).Delay = 'PT2S' },
        { param($d) $null = $d.Actions.Create(0) }
    )
    foreach ($mutation in $mutations) {
        $drift = $service.NewTask(0)
        $drift.XmlText = $definition.XmlText
        & $mutation $drift
        $refused = $false
        try { $null = Get-OwnedTask $folder $name $drift $sddl } catch {
            if ($_.Exception.Message -notlike '*non-matching Harbor daemon task definition*') { throw }
            $refused = $true
        }
        if (-not $refused) { throw 'drifted definition admitted' }
    }
    $refused = $false
    try { $null = Get-OwnedTask $folder $name $definition ($sddl + '(A;;FA;;;BU)') } catch {
        if ($_.Exception.Message -notlike '*non-matching Harbor daemon task security descriptor*') { throw }
        $refused = $true
    }
    if (-not $refused) { throw 'drifted ACL admitted' }
    $task = Get-OwnedTask $folder $name $definition $sddl
    if ((Convert-TaskXML $task.Definition.XmlText) -cne $originalXML -or (Convert-TaskACL $task.GetSecurityDescriptor(7)) -cne $originalACL) { throw 'foreign comparison changed task' }
    $collisionRefused = $false
    try { $null = $folder.RegisterTaskDefinition($name, $definition, 18, $sid, $null, 3, $sddl) } catch { $collisionRefused = $true }
    if (-not $collisionRefused) { throw 'create-only registration replaced an existing task' }
    $null = Get-OwnedTask $folder $name $definition $sddl
    $folder.DeleteTask($name, 0)
    $created = $false
    if ($null -ne (Get-OwnedTask $folder $name $definition $sddl)) { throw 'deleted fixture still exists' }
    $unconnected = [Activator]::CreateInstance([Type]::GetTypeFromProgID('Schedule.Service'))
    $errorPreserved = $false
    try { $null = Get-OwnedTask $unconnected $name $definition $sddl } catch { $errorPreserved = $true }
    if (-not $errorPreserved) { throw 'COM observation error reported as absence' }
} finally {
    if ($created) {
        # Retain the original registration receipt even if expected COM normalization fails admission.
        $remaining = $folder.GetTask($name)
        if ((Convert-TaskXML $remaining.Definition.XmlText) -cne $createdXML -or (Convert-TaskACL $remaining.GetSecurityDescriptor(7)) -cne $createdACL -or $remaining.GetInstances(0).Count -ne 0) { throw 'preserve changed native task fixture' }
        $folder.DeleteTask($name, 0)
    }
}
Write-Output 'native task scheduler admission passed'
`)
}

// runTaskPolicyFixture loads production helpers without invoking fixed-root product policy or accepting caller task paths.
func runTaskPolicyFixture(t *testing.T, fixture string) {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "windows", "harbor-daemon-task.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	_, helpers, ok := strings.Cut(string(source), "\n")
	if !ok {
		t.Fatal("task policy has no parameter preamble")
	}
	helpers, _, ok = strings.Cut(helpers, "# Production task policy starts here.")
	if !ok {
		t.Fatal("task policy function boundary missing")
	}
	executable, err := taskPowerShell()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	path := filepath.Join(t.TempDir(), "task-native-fixture.ps1")
	if err := os.WriteFile(path, []byte("try {\n"+helpers+fixture+"\n} catch { [Console]::Error.WriteLine($_); exit 1 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, executable, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path)
	command.Env = taskEnvironment(os.Environ())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native task fixture: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "passed") {
		t.Fatalf("native fixture did not finish: %s", output)
	}
	t.Log(strings.TrimSpace(string(output)))
}
