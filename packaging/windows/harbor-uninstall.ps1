param(
    [switch]$ValidateOnly
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
$PSModuleAutoLoadingPreference = "None"

if (-not $ValidateOnly) {
    throw "Use Harbor's admitted native uninstaller to remove this installation."
}

# Uninstall owns Harbor resources, not the publisher's public certificate trust.
foreach ($location in @(
    [Security.Cryptography.X509Certificates.StoreLocation]::LocalMachine,
    [Security.Cryptography.X509Certificates.StoreLocation]::CurrentUser
)) {
    $store = [Security.Cryptography.X509Certificates.X509Store]::new(
        [Security.Cryptography.X509Certificates.StoreName]::Root,
        $location
    )
    try {
        $store.Open([Security.Cryptography.X509Certificates.OpenFlags]::ReadOnly)
        foreach ($certificate in $store.Certificates) {
            if ($certificate.FriendlyName -like "goforj.harbor.windows-machine-root.v1|*" -or
                $certificate.FriendlyName -like "goforj.harbor.windows-current-user-root.v1|*") {
                throw "Harbor trust remains active; release Harbor networking before uninstalling."
            }
        }
    }
    finally {
        $store.Dispose()
    }
}

# Resolve the inbox module from Windows, so administrator validation never autoloads caller modules.
$dnsModule = [IO.Path]::Combine(
    [Environment]::SystemDirectory,
    "WindowsPowerShell\v1.0\Modules\DnsClient\DnsClient.psd1"
)
Import-Module -Name $dnsModule -ErrorAction Stop
foreach ($rule in Get-DnsClientNrptRule) {
    if ($rule.DisplayName -like "GoForj Harbor Resolver *" -or
        $rule.Comment -like "goforj.harbor.resolver *") {
        throw "Harbor name resolution remains active; release Harbor networking before uninstalling."
    }
}
