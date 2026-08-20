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

; This is a 32-bit installer writing 64-bit machine state: without SetRegView 64
; every HKLM\Software write is redirected into WOW6432Node, and the all-users
; context is needed so the Start-menu group lands in ProgramData rather than in
; whichever profile happened to approve the UAC prompt.
Function .onInit
  SetRegView 64
  SetShellVarContext all
FunctionEnd

Function un.onInit
  SetRegView 64
  SetShellVarContext all
FunctionEnd

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
  Call AddInstDirToPath
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
  Call un.RemoveInstDirFromPath
SectionEnd

; --- PATH helpers -----------------------------------------------------------
;
; Deliberately built from core instructions (StrLen/StrCpy/IntOp) instead of
; StrFunc: a mis-declared StrFunc macro expands to nothing, leaves the result
; register holding whatever the previous section left there, and the caller
; "successfully" skips the write. That is not a failure mode an installer should
; have.
!define ENV_HKLM 'HKLM "SYSTEM\CurrentControlSet\Control\Session Manager\Environment"'

; PathContains: $0 = haystack, $1 = needle -> $2 = "1" when present. Only the
; installer needs it; the uninstaller rebuilds PATH entry-wise instead.
!macro PathContainsBody UN
Function ${UN}PathContains
  Push $3
  Push $4
  Push $5
  StrCpy $2 "0"
  StrLen $3 "$1"
  StrLen $4 "$0"
  IntOp $4 $4 - $3
  IntCmp $4 0 0 done 0   ; needle longer than haystack -> not present
  StrCpy $5 0
  loop:
    StrCpy $6 "$0" $3 $5
    StrCmp "$6" "$1" found
    IntOp $5 $5 + 1
    IntCmp $5 $4 loop loop done
  found:
    StrCpy $2 "1"
  done:
  Pop $5
  Pop $4
  Pop $3
FunctionEnd
!macroend
!insertmacro PathContainsBody ""

Function AddInstDirToPath
  Push $0
  Push $1
  Push $2
  ReadRegStr $0 ${ENV_HKLM} "Path"
  StrCpy $1 "$INSTDIR"
  Call PathContains
  StrCmp $2 "1" done
    StrCmp "$0" "" 0 +3
      StrCpy $0 "$INSTDIR"
      Goto write
    StrCpy $0 "$0;$INSTDIR"
  write:
    WriteRegExpandStr ${ENV_HKLM} "Path" "$0"
    ; Tell running shells to re-read the environment; without this a new
    ; terminal still would not find the CLI until the next sign-in.
    SendMessage ${HWND_BROADCAST} ${WM_WININICHANGE} 0 "STR:Environment" /TIMEOUT=5000
  done:
  Pop $2
  Pop $1
  Pop $0
FunctionEnd

; un.RemoveInstDirFromPath rebuilds PATH from its entries, dropping ours. Doing
; it entry-wise rather than by string replacement cannot corrupt a neighbouring
; directory whose name happens to contain ours.
Function un.RemoveInstDirFromPath
  Push $0
  Push $1
  Push $2
  Push $3
  Push $4
  ReadRegStr $0 ${ENV_HKLM} "Path"
  StrCpy $1 ""      ; rebuilt PATH
  StrCpy $2 ""      ; current entry
  StrCpy $3 0       ; cursor
  loop:
    StrCpy $4 "$0" 1 $3
    StrCmp "$4" "" flush
    StrCmp "$4" ";" flush
      StrCpy $2 "$2$4"
      IntOp $3 $3 + 1
      Goto loop
  flush:
    StrCmp "$2" "$INSTDIR" skip
    StrCmp "$2" "" skip
      StrCmp "$1" "" 0 +3
        StrCpy $1 "$2"
        Goto skip
      StrCpy $1 "$1;$2"
  skip:
    StrCpy $2 ""
    StrCmp "$4" "" written
    IntOp $3 $3 + 1
    Goto loop
  written:
  WriteRegExpandStr ${ENV_HKLM} "Path" "$1"
  SendMessage ${HWND_BROADCAST} ${WM_WININICHANGE} 0 "STR:Environment" /TIMEOUT=5000
  Pop $4
  Pop $3
  Pop $2
  Pop $1
  Pop $0
FunctionEnd
