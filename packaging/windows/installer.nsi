Unicode true

!include "MUI2.nsh"
!include "LogicLib.nsh"

!ifndef HARBOR_COMPONENT_ROOT
  !error "HARBOR_COMPONENT_ROOT is required"
!endif
!ifndef HARBOR_PACKAGE_PATH
  !error "HARBOR_PACKAGE_PATH is required"
!endif
!ifndef HARBOR_DISPLAY_VERSION
  !error "HARBOR_DISPLAY_VERSION is required"
!endif
!ifndef HARBOR_FILE_VERSION
  !error "HARBOR_FILE_VERSION is required"
!endif

Name "GoForj Harbor"
OutFile "${HARBOR_PACKAGE_PATH}"
InstallDir "$PROGRAMFILES64\GoForj\Harbor"
InstallDirRegKey HKLM "Software\GoForj\Harbor" "InstallLocation"
RequestExecutionLevel admin
SetCompressor /SOLID lzma
ShowInstDetails show
ShowUninstDetails show

VIProductVersion "${HARBOR_FILE_VERSION}"
VIFileVersion "${HARBOR_FILE_VERSION}"
VIAddVersionKey "CompanyName" "GoForj"
VIAddVersionKey "FileDescription" "GoForj Harbor Installer"
VIAddVersionKey "ProductVersion" "${HARBOR_DISPLAY_VERSION}"
VIAddVersionKey "FileVersion" "${HARBOR_DISPLAY_VERSION}"
VIAddVersionKey "ProductName" "GoForj Harbor"

!define MUI_ABORTWARNING
!define MUI_ICON "${HARBOR_COMPONENT_ROOT}\icon.ico"
!define MUI_UNICON "${HARBOR_COMPONENT_ROOT}\icon.ico"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "GoForj Harbor" SEC_HARBOR
  SetShellVarContext all
  SetRegView 64
  SetOutPath "$INSTDIR"
  File /oname=harbor-desktop.exe "${HARBOR_COMPONENT_ROOT}\harbor-desktop.exe"
  File /oname=harbor.exe "${HARBOR_COMPONENT_ROOT}\harbor.exe"
  File /oname=harbord.exe "${HARBOR_COMPONENT_ROOT}\harbord.exe"
  File /oname=outputbroker.exe "${HARBOR_COMPONENT_ROOT}\outputbroker.exe"
  File /oname=harbor-helper.exe "${HARBOR_COMPONENT_ROOT}\harbor-helper.exe"
  File /oname=harbor-uninstall.ps1 "${HARBOR_COMPONENT_ROOT}\harbor-uninstall.ps1"
  WriteUninstaller "$INSTDIR\Uninstall.exe"

  CreateDirectory "$SMPROGRAMS\GoForj"
  CreateShortcut "$SMPROGRAMS\GoForj\GoForj Harbor.lnk" "$INSTDIR\harbor-desktop.exe"

  WriteRegStr HKLM "Software\GoForj\Harbor" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoForjHarbor" "DisplayName" "GoForj Harbor"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoForjHarbor" "DisplayVersion" "${HARBOR_DISPLAY_VERSION}"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoForjHarbor" "Publisher" "GoForj"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoForjHarbor" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoForjHarbor" "UninstallString" "$\"$INSTDIR\Uninstall.exe$\""
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoForjHarbor" "NoModify" 1
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoForjHarbor" "NoRepair" 1
SectionEnd

Section "Uninstall"
  SetShellVarContext all
  SetRegView 64
  ExecWait '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$INSTDIR\harbor-uninstall.ps1"' $0
  ${If} $0 != 0
    DetailPrint "Harbor host-state cleanup refused removal with exit code $0."
    SetErrorLevel $0
    Quit
  ${EndIf}

  Delete "$SMPROGRAMS\GoForj\GoForj Harbor.lnk"
  RMDir "$SMPROGRAMS\GoForj"
  Delete "$INSTDIR\harbor-desktop.exe"
  Delete "$INSTDIR\harbor.exe"
  Delete "$INSTDIR\harbord.exe"
  Delete "$INSTDIR\outputbroker.exe"
  Delete "$INSTDIR\harbor-helper.exe"
  Delete "$INSTDIR\harbor-uninstall.ps1"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"

  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\GoForjHarbor"
  DeleteRegKey HKLM "Software\GoForj\Harbor"
  DeleteRegKey /ifempty HKLM "Software\GoForj"
SectionEnd
