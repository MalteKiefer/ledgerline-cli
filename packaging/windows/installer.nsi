; Windows installer for Ledgerline: the CLI and the tray GUI in one setup.
;
; Built by `make installer-windows`, which passes the version and the binary
; paths in with /D so this script stays free of build-time knowledge:
;   makensis -DVERSION=0.7.5 \
;            -DCLI_EXE=../../dist/ledgerline-cli-0.7.5-windows-amd64.exe \
;            -DGUI_EXE=../../dist/ledgerline-gui-0.7.5-windows-amd64.exe \
;            -DOUTFILE=../../dist/ledgerline-setup-0.7.5-amd64.exe installer.nsi
;
; NSIS is used rather than WiX/MSI because it cross-builds on the Linux release
; runner with no Windows tooling. An MSI would be the answer if Group Policy
; deployment is ever needed; that is a separate decision, not a default.

Unicode true
ManifestDPIAware true

!include "MUI2.nsh"
!include "FileFunc.nsh"
!include "WinMessages.nsh"
!include "StrFunc.nsh"

; StrFunc requires each helper to be "declared" once outside a section, and the
; uninstaller needs its own Un-prefixed copy.
${StrStr}
${UnStrRep}

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef OUTFILE
  !define OUTFILE "ledgerline-setup.exe"
!endif

!define APPNAME "Ledgerline"
!define PUBLISHER "Malte Kiefer"
!define HOMEPAGE "https://github.com/MalteKiefer/ledgerline-cli"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPNAME}"

Name "${APPNAME} ${VERSION}"
OutFile "${OUTFILE}"
InstallDir "$PROGRAMFILES64\${APPNAME}"
InstallDirRegKey HKLM "Software\${APPNAME}" "InstallDir"
; Writing to Program Files and HKLM needs elevation; asking up front beats
; failing halfway through a copy.
RequestExecutionLevel admin
SetCompressor /SOLID lzma

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APPNAME}"
VIAddVersionKey "FileDescription" "${APPNAME} desktop client"
VIAddVersionKey "CompanyName" "${PUBLISHER}"
VIAddVersionKey "LegalCopyright" "© 2026 ${PUBLISHER}"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"

!define MUI_ABORTWARNING
!insertmacro MUI_PAGE_LICENSE "..\..\LICENSE"
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\ledgerline-gui.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Start the ${APPNAME} tray icon now"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "${APPNAME} (required)" SecCore
  SectionIn RO
  SetOutPath "$INSTDIR"
  File /oname=ledgerline-cli.exe "${CLI_EXE}"
  File /oname=ledgerline-gui.exe "${GUI_EXE}"
  File "..\..\LICENSE"
  File "..\..\README.md"

  WriteRegStr HKLM "Software\${APPNAME}" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "Software\${APPNAME}" "Version" "${VERSION}"

  ; Add/Remove Programs entry, including the size so Windows reports it
  ; honestly rather than as unknown.
  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr   HKLM "${UNINSTKEY}" "DisplayName"     "${APPNAME}"
  WriteRegStr   HKLM "${UNINSTKEY}" "DisplayVersion"  "${VERSION}"
  WriteRegStr   HKLM "${UNINSTKEY}" "Publisher"       "${PUBLISHER}"
  WriteRegStr   HKLM "${UNINSTKEY}" "URLInfoAbout"    "${HOMEPAGE}"
  WriteRegStr   HKLM "${UNINSTKEY}" "DisplayIcon"     "$INSTDIR\ledgerline-gui.exe"
  WriteRegStr   HKLM "${UNINSTKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr   HKLM "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKLM "${UNINSTKEY}" "EstimatedSize" "$0"
SectionEnd

Section "Start menu shortcuts" SecStartMenu
  CreateDirectory "$SMPROGRAMS\${APPNAME}"
  CreateShortcut "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk" "$INSTDIR\ledgerline-gui.exe" "" "$INSTDIR\ledgerline-gui.exe" 0
  ; A shell in the install directory: the CLI is the tool most of this product
  ; is, and a Start-menu entry saves hunting for a terminal.
  CreateShortcut "$SMPROGRAMS\${APPNAME}\${APPNAME} CLI.lnk" "$SYSDIR\cmd.exe" '/K "cd /d %USERPROFILE% && ledgerline-cli --help"' "$INSTDIR\ledgerline-cli.exe" 0
  CreateShortcut "$SMPROGRAMS\${APPNAME}\Uninstall ${APPNAME}.lnk" "$INSTDIR\uninstall.exe"
SectionEnd

Section "Add to PATH (all users)" SecPath
  ; Append rather than replace, and only when not already present, so an
  ; upgrade or a re-run cannot duplicate or clobber the machine PATH.
  ReadRegStr $0 HKLM "SYSTEM\CurrentControlSet\Control\Session Manager\Environment" "Path"
  ${StrStr} $1 "$0" "$INSTDIR"
  StrCmp $1 "" 0 pathDone
    StrCpy $2 "$0"
    StrCmp $2 "" 0 +2
      StrCpy $2 "$INSTDIR"
      Goto writePath
    StrCpy $2 "$0;$INSTDIR"
  writePath:
    WriteRegExpandStr HKLM "SYSTEM\CurrentControlSet\Control\Session Manager\Environment" "Path" "$2"
    SendMessage ${HWND_BROADCAST} ${WM_WININICHANGE} 0 "STR:Environment" /TIMEOUT=5000
  pathDone:
SectionEnd

Section /o "Start the tray icon at sign-in" SecAutostart
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "${APPNAME}" '"$INSTDIR\ledgerline-gui.exe"'
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecCore} "The ledgerline-cli command and the ledgerline-gui tray icon."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecStartMenu} "Shortcuts for the tray icon, a CLI shell and the uninstaller."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecPath} "Make ledgerline-cli runnable from any terminal."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecAutostart} "Run the tray icon automatically when you sign in."
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Section "Uninstall"
  ; Stop the tray first: an open handle on the .exe would leave it behind.
  nsExec::Exec 'taskkill /IM ledgerline-gui.exe /F'

  Delete "$INSTDIR\ledgerline-cli.exe"
  Delete "$INSTDIR\ledgerline-gui.exe"
  Delete "$INSTDIR\LICENSE"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  Delete "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk"
  Delete "$SMPROGRAMS\${APPNAME}\${APPNAME} CLI.lnk"
  Delete "$SMPROGRAMS\${APPNAME}\Uninstall ${APPNAME}.lnk"
  RMDir "$SMPROGRAMS\${APPNAME}"

  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "${APPNAME}"
  DeleteRegKey HKLM "${UNINSTKEY}"
  DeleteRegKey HKLM "Software\${APPNAME}"

  ; The install directory is removed from PATH, but the user's credential and
  ; config directory is deliberately left alone: uninstalling the program must
  ; not silently destroy a device pairing or a sync ledger.
  ReadRegStr $0 HKLM "SYSTEM\CurrentControlSet\Control\Session Manager\Environment" "Path"
  ${UnStrRep} $1 "$0" ";$INSTDIR" ""
  ${UnStrRep} $1 "$1" "$INSTDIR;" ""
  ${UnStrRep} $1 "$1" "$INSTDIR" ""
  WriteRegExpandStr HKLM "SYSTEM\CurrentControlSet\Control\Session Manager\Environment" "Path" "$1"
  SendMessage ${HWND_BROADCAST} ${WM_WININICHANGE} 0 "STR:Environment" /TIMEOUT=5000
SectionEnd
