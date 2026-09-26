; SuperSync installer for Windows. Built by scripts/build.sh:
;   makensis -DVERSION=v0.1.2 -DSRC=<dir with SuperSync.exe> -DOUT=<setup.exe> installer.nsi
Unicode true
!include "MUI2.nsh"

Name "SuperSync"
OutFile "${OUT}"
InstallDir "$LOCALAPPDATA\Programs\SuperSync"
InstallDirRegKey HKCU "Software\SuperSync" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma
BrandingText "SuperSync ${VERSION}"

!define UNINST "Software\Microsoft\Windows\CurrentVersion\Uninstall\SuperSync"
!define MUI_ICON "${SRC}\SuperSync.ico"
!define MUI_UNICON "${SRC}\SuperSync.ico"
!define MUI_WELCOMEPAGE_TITLE "Install SuperSync"
!define MUI_WELCOMEPAGE_TEXT "SuperSync keeps your rekordbox library in step with your SoundCloud playlists, without the duplicates.$\r$\n$\r$\nIt installs just for you, so no administrator password is needed.$\r$\n$\r$\nClick Next to install."
!define MUI_FINISHPAGE_RUN "$INSTDIR\SuperSync.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Open SuperSync"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "SuperSync"
  ; Close a running copy so its file can be replaced: ask it to quit first
  ; (it finishes any library write), and only force it if it's still there.
  IfFileExists "$INSTDIR\SuperSync.exe" 0 +2
    nsExec::Exec '"$INSTDIR\SuperSync.exe" quit'
  nsExec::Exec 'taskkill /IM SuperSync.exe /F'
  Sleep 500
  SetOutPath "$INSTDIR"
  File "${SRC}\SuperSync.exe"
  File "${SRC}\SuperSync.ico"
  WriteUninstaller "$INSTDIR\Uninstall SuperSync.exe"

  CreateShortcut "$SMPROGRAMS\SuperSync.lnk" "$INSTDIR\SuperSync.exe" "" "$INSTDIR\SuperSync.ico"
  CreateShortcut "$DESKTOP\SuperSync.lnk" "$INSTDIR\SuperSync.exe" "" "$INSTDIR\SuperSync.ico"

  WriteRegStr HKCU "Software\SuperSync" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINST}" "DisplayName" "SuperSync"
  WriteRegStr HKCU "${UNINST}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST}" "Publisher" "SuperSync"
  WriteRegStr HKCU "${UNINST}" "DisplayIcon" "$INSTDIR\SuperSync.ico"
  WriteRegStr HKCU "${UNINST}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST}" "UninstallString" '"$INSTDIR\Uninstall SuperSync.exe"'
  WriteRegStr HKCU "${UNINST}" "URLInfoAbout" "https://github.com/diva-bot-create/SuperSync"
  WriteRegDWORD HKCU "${UNINST}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST}" "NoRepair" 1
SectionEnd

; Removes the app and its shortcuts. Your settings, library backups and music
; are left alone.
Section "Uninstall"
  nsExec::Exec '"$INSTDIR\SuperSync.exe" quit'
  nsExec::Exec 'taskkill /IM SuperSync.exe /F'
  Sleep 500
  ; Stop opening at login.
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "SuperSync"
  Delete "$INSTDIR\SuperSync.exe"
  Delete "$INSTDIR\SuperSync.exe.old"
  Delete "$INSTDIR\SuperSync.ico"
  Delete "$INSTDIR\Uninstall SuperSync.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\SuperSync.lnk"
  Delete "$DESKTOP\SuperSync.lnk"
  DeleteRegKey HKCU "${UNINST}"
  DeleteRegKey HKCU "Software\SuperSync"
SectionEnd
