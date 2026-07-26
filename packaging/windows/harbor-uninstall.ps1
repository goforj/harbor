$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$machineRoots = @(Get-ChildItem -LiteralPath "Cert:\LocalMachine\Root" | Where-Object {
    $_.FriendlyName -like "goforj.harbor.windows-machine-root.v1|*"
})
$userRoots = @(Get-ChildItem -LiteralPath "Cert:\CurrentUser\Root" | Where-Object {
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
