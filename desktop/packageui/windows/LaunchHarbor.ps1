param(
    [Parameter(Mandatory=$true)][string]$DesktopPath,
    [Parameter(Mandatory=$true)][string]$ToolsPath
)
$ErrorActionPreference = 'Stop'
$env:PATH = "$ToolsPath;$env:PATH"
Start-Process -FilePath $DesktopPath -WorkingDirectory (Split-Path $DesktopPath -Parent)
