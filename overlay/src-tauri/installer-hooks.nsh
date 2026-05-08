; NSIS installer/uninstaller hooks for RLT-Launcher.
;
; Why these exist: the launcher bundles rl-toolkit.exe as an
; externalBin sidecar. Tauri's standard NSIS template kills the main
; binary (RLT-Launcher.exe) before uninstall/upgrade, but it does not
; know about the sidecar. If the launcher quits abnormally (taskkill,
; crash) the sidecar can outlive its parent — leaving rl-toolkit.exe
; running after uninstall, or holding rl-toolkit.exe locked during a
; reinstall. We taskkill it explicitly here.
;
; nsExec::Exec runs hidden (no console flash). /F forces termination,
; /T kills the whole process tree (any grandchildren the sidecar
; might have spawned). Errors are intentionally ignored: if the
; sidecar isn't running, taskkill returns non-zero and we move on.

!macro NSIS_HOOK_PREINSTALL
  nsExec::Exec 'taskkill /F /T /IM rl-toolkit.exe'
  Pop $0
!macroend

!macro NSIS_HOOK_PREUNINSTALL
  nsExec::Exec 'taskkill /F /T /IM rl-toolkit.exe'
  Pop $0
!macroend
