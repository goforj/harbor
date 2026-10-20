param([Parameter(Mandatory=$true)][string]$EvidenceDirectory)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'WorkerProfile.ps1')
Assert-InteractiveWorker
$nonce = [Guid]::NewGuid().ToString('N')
$programRoot = Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::ProgramFiles)) "HarborUIProfile-$nonce"
$machineRoot = Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::CommonApplicationData)) "HarborUIProfile-$nonce"
$registryName = "SOFTWARE\HarborUIProfile-$nonce"
$registryPath = "HKLM:\$registryName"
$receipt = [ordered]@{ profile='hosted-preauthorized-restricted-functional'; source_token=[HarborUIProfile]::CurrentTokenFacts(); child_token=$null; access_receipts=@(); complete=$false; quiescent=$false; fixture_removed=$false }
New-Item -ItemType Directory -Path $EvidenceDirectory -Force | Out-Null
try {
    foreach ($path in @($programRoot, $machineRoot)) {
        if (Test-Path -LiteralPath $path) { throw 'profile fixture unexpectedly exists' }
        New-Item -ItemType Directory -Path $path | Out-Null
        $security = [Security.AccessControl.DirectorySecurity]::new()
        $security.SetSecurityDescriptorSddlForm('O:BAD:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)(A;OICI;GRGX;;;BU)')
        Set-Acl -LiteralPath $path -AclObject $security
    }
    $file = Join-Path $programRoot 'protected.txt'
    [IO.File]::WriteAllText($file, 'protected fixture must stay unchanged')
    $fileSecurity = [Security.AccessControl.FileSecurity]::new()
    $fileSecurity.SetSecurityDescriptorSddlForm('O:BAD:P(A;;FA;;;BA)(A;;FA;;;SY)(A;;GRGX;;;BU)')
    Set-Acl -LiteralPath $file -AclObject $fileSecurity
    $fileHash = (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash
    if (Test-Path -LiteralPath $registryPath) { throw 'profile registry fixture unexpectedly exists' }
    New-Item -Path $registryPath | Out-Null
    $registrySecurity = [Security.AccessControl.RegistrySecurity]::new()
    $registrySecurity.SetSecurityDescriptorSddlForm('O:BAD:P(A;CI;KA;;;BA)(A;CI;KA;;;SY)(A;CI;KR;;;BU)')
    Set-Acl -LiteralPath $registryPath -AclObject $registrySecurity
    [HarborUIProfile]::ProtectedDirectories = [string[]]@($programRoot, $machineRoot)
    [HarborUIProfile]::ProtectedFiles = [string[]]@($file)
    [HarborUIProfile]::ProtectedRegistryKey = $registryName
    $application = Join-Path $env:SystemRoot 'System32\whoami.exe'
    $null = [HarborUIProfile]::Launch($application, (Join-Path $env:SystemRoot 'System32'))
    if ((Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash -cne $fileHash) { throw 'profile altered protected fixture bytes' }
    $receipt.complete = $true
} finally {
    $receipt.child_token = [HarborUIProfile]::LastLaunchFacts
    $receipt.access_receipts = @([HarborUIProfile]::AccessReceipts)
    try { [HarborUIProfile]::QuiesceLaunchedUIs(); $receipt.quiescent = $true } finally { [HarborUIProfile]::CloseLaunchReceipts() }
    foreach ($path in @($programRoot, $machineRoot)) { if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Recurse } }
    if (Test-Path -LiteralPath $registryPath) { Remove-Item -LiteralPath $registryPath -Recurse }
    $receipt.fixture_removed = -not ((Test-Path -LiteralPath $programRoot) -or (Test-Path -LiteralPath $machineRoot) -or (Test-Path -LiteralPath $registryPath))
    $receipt | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $EvidenceDirectory 'worker-profile.json') -Encoding utf8
}
