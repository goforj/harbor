param(
    [switch]$ValidateOnly
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$installRoot = $PSScriptRoot
$diagnosticPath = Join-Path $installRoot "harbor-uninstall-error.txt"
trap {
    $message = $_.Exception.Message -replace "[`r`n]+", " "
    if ($message.Length -gt 2048) {
        $message = $message.Substring(0, 2048)
    }
    $trapDiagnosticPath = Join-Path $PSScriptRoot "harbor-uninstall-error.txt"
    [IO.File]::WriteAllText($trapDiagnosticPath, $message, [Text.UTF8Encoding]::new($false))
    [Console]::Error.WriteLine($message)
    exit 1
}
Remove-Item -LiteralPath $diagnosticPath -Force -ErrorAction SilentlyContinue

function Get-StoreCertificates {
    param(
        [Security.Cryptography.X509Certificates.StoreName]$Name,
        [Security.Cryptography.X509Certificates.StoreLocation]$Location
    )

    $store = [Security.Cryptography.X509Certificates.X509Store]::new($Name, $Location)
    try {
        $store.Open([Security.Cryptography.X509Certificates.OpenFlags]::ReadOnly)
        return @($store.Certificates)
    }
    finally {
        $store.Dispose()
    }
}

function Remove-StoreCertificate {
    param(
        [Security.Cryptography.X509Certificates.StoreName]$Name,
        [Security.Cryptography.X509Certificates.StoreLocation]$Location,
        [string]$Thumbprint
    )

    $store = [Security.Cryptography.X509Certificates.X509Store]::new($Name, $Location)
    try {
        $store.Open([Security.Cryptography.X509Certificates.OpenFlags]::ReadWrite)
        $matches = $store.Certificates.Find(
            [Security.Cryptography.X509Certificates.X509FindType]::FindByThumbprint,
            $Thumbprint,
            $false
        )
        foreach ($certificate in $matches) {
            $store.Remove($certificate)
        }
    }
    finally {
        $store.Dispose()
    }
}

$signerCertificatePath = Join-Path $installRoot "harbor-helper-signing.cer"
$signerThumbprintPath = Join-Path $installRoot "harbor-helper-signing-thumbprint.txt"
if (-not (Test-Path -LiteralPath $signerCertificatePath -PathType Leaf) -or
    -not (Test-Path -LiteralPath $signerThumbprintPath -PathType Leaf)) {
    throw "Harbor helper signing identity is missing."
}
$signerThumbprint = [IO.File]::ReadAllText($signerThumbprintPath).Trim()
if ($signerThumbprint -cnotmatch "^[0-9A-F]{40}$") {
    throw "Harbor helper signing thumbprint is invalid."
}
$sealedCertificate = [Security.Cryptography.X509Certificates.X509Certificate2]::new($signerCertificatePath)
if ($sealedCertificate.Thumbprint -cne $signerThumbprint) {
    throw "Harbor helper signing certificate differs from its sealed identity."
}
$signerStores = @(
    [Security.Cryptography.X509Certificates.StoreName]::Root,
    [Security.Cryptography.X509Certificates.StoreName]::TrustedPublisher
)
foreach ($storeName in $signerStores) {
    $installedCertificates = @(Get-StoreCertificates `
        -Name $storeName `
        -Location ([Security.Cryptography.X509Certificates.StoreLocation]::LocalMachine) |
        Where-Object { $_.Thumbprint -ceq $signerThumbprint })
    if ($installedCertificates.Count -eq 0) {
        throw "Harbor helper signing certificate is missing from LocalMachine\$storeName."
    }
}

$machineRoots = @(Get-StoreCertificates `
    -Name ([Security.Cryptography.X509Certificates.StoreName]::Root) `
    -Location ([Security.Cryptography.X509Certificates.StoreLocation]::LocalMachine) | Where-Object {
    $_.FriendlyName -like "goforj.harbor.windows-machine-root.v1|*"
})
$userRoots = @(Get-StoreCertificates `
    -Name ([Security.Cryptography.X509Certificates.StoreName]::Root) `
    -Location ([Security.Cryptography.X509Certificates.StoreLocation]::CurrentUser) | Where-Object {
    $_.FriendlyName -like "goforj.harbor.windows-current-user-root.v1|*"
})
if ($machineRoots.Count -ne 0 -or $userRoots.Count -ne 0) {
    throw "Harbor trust remains active; release Harbor networking before uninstalling."
}

$harborRules = @(Get-DnsClientNrptRule | Where-Object {
    $_.DisplayName -like "GoForj Harbor Resolver *" -or
    $_.Comment -like "goforj.harbor.resolver *"
})
if ($harborRules.Count -ne 0) {
    throw "Harbor name resolution remains active; release Harbor networking before uninstalling."
}

$programData = [Environment]::GetFolderPath([Environment+SpecialFolder]::CommonApplicationData)
$privilegedRoot = Join-Path $programData "GoForj\Harbor\Privileged"
if (Test-Path -LiteralPath $privilegedRoot) {
    $rootItem = Get-Item -LiteralPath $privilegedRoot -Force
    if (-not $rootItem.PSIsContainer -or ($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw "Harbor privileged root has a foreign filesystem identity."
    }

    $allowedDirectories = @(
        "",
        "state",
        "state\replay",
        "tickets",
        "tickets\claims",
        "tickets\pending"
    )
    $rootPrefix = $privilegedRoot.TrimEnd("\") + "\"
    foreach ($item in Get-ChildItem -LiteralPath $privilegedRoot -Force -Recurse) {
        if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
            throw "Harbor privileged state contains a reparse point: $($item.FullName)"
        }
        if (-not $item.FullName.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Harbor privileged state escaped its fixed root."
        }
        $relative = $item.FullName.Substring($rootPrefix.Length)
        if ($item.PSIsContainer) {
            if ($relative -notin $allowedDirectories) {
                throw "Harbor privileged state contains an unrecognized directory: $relative"
            }
            continue
        }
        $admittedFile = (
            $relative -eq "ownership-release-proof.json" -or
            $relative -eq "ownership-release-proof.lock" -or
            $relative -eq "state\ownership.json.lock" -or
            $relative -eq "state\host-projection.json.lock" -or
            $relative -like "state\replay\*" -or
            $relative -like "tickets\claims\*"
        )
        if (-not $admittedFile -or ($relative -split "\\").Count -gt 3) {
            throw "Harbor privileged state contains an unrecognized file: $relative"
        }
    }

    $pending = Join-Path $privilegedRoot "tickets\pending"
    if ((Test-Path -LiteralPath $pending) -and
        @(Get-ChildItem -LiteralPath $pending -Force).Count -ne 0) {
        throw "Harbor still has pending privileged operations."
    }
}

if ($ValidateOnly) {
    exit 0
}

foreach ($storeName in $signerStores) {
    Remove-StoreCertificate `
        -Name $storeName `
        -Location ([Security.Cryptography.X509Certificates.StoreLocation]::LocalMachine) `
        -Thumbprint $signerThumbprint
}
if (Test-Path -LiteralPath $privilegedRoot) {
    Remove-Item -LiteralPath $privilegedRoot -Recurse -Force
}

$harborData = Join-Path $programData "GoForj\Harbor"
$goforjData = Join-Path $programData "GoForj"
if ((Test-Path -LiteralPath $harborData) -and
    @(Get-ChildItem -LiteralPath $harborData -Force).Count -eq 0) {
    Remove-Item -LiteralPath $harborData -Force
}
if ((Test-Path -LiteralPath $goforjData) -and
    @(Get-ChildItem -LiteralPath $goforjData -Force).Count -eq 0) {
    Remove-Item -LiteralPath $goforjData -Force
}
