# Build both 0.1.0 and 0.2.0 installers in one go for the auto-update
# smoke test. Run from the repo root on a Windows host. Requires:
#
#   $env:TAURI_SIGNING_PRIVATE_KEY              the .key file contents
#   $env:TAURI_SIGNING_PRIVATE_KEY_PASSWORD     the matching password
#   $env:RELEASE_OWNER                          GitHub owner; default s1gh
#
# What it does (in order):
#   1. Build 0.1.0  -> stage in dist/smoketest/0.1.0/
#   2. Bump version files in place -> 0.2.0
#   3. Build 0.2.0  -> stage in dist/smoketest/0.2.0/
#   4. Generate latest.json (BOM-free, via -out flag)
#   5. Revert version files back to 0.1.0
#
# After it finishes, you have:
#   dist/smoketest/0.1.0/ RLT-Launcher_0.1.0_x64-setup.exe (+ .sig)
#   dist/smoketest/0.2.0/ RLT-Launcher_0.2.0_x64-setup.exe (+ .sig + latest.json)
#
# Upload everything in 0.1.0/ to the v0.1.0 GitHub release. Upload
# everything in 0.2.0/ to the v0.2.0 release. Done.

$ErrorActionPreference = "Stop"

if (-not $env:TAURI_SIGNING_PRIVATE_KEY -or -not $env:TAURI_SIGNING_PRIVATE_KEY_PASSWORD) {
  Write-Error "Set TAURI_SIGNING_PRIVATE_KEY and TAURI_SIGNING_PRIVATE_KEY_PASSWORD before running."
}

$owner = if ($env:RELEASE_OWNER) { $env:RELEASE_OWNER } else { "s1gh" }
$cargoToml = "overlay\src-tauri\Cargo.toml"
$tauriConf = "overlay\src-tauri\tauri.conf.json"
$bundleDir = "overlay\src-tauri\target\release\bundle\nsis"
$stageRoot = "dist\smoketest"

function Set-Version([string]$ver) {
  (Get-Content $cargoToml) -replace '^version = "[^"]*"', ('version = "{0}"' -f $ver) |
    Set-Content -NoNewline $cargoToml
  # Cargo.toml requires trailing newline; -replace strips none, but
  # Set-Content -NoNewline drops the EOF newline. Re-append it.
  Add-Content -NoNewline $cargoToml "`n"
  (Get-Content $tauriConf) -replace '"version": "[^"]*"', ('"version": "{0}"' -f $ver) |
    Set-Content $tauriConf
  Write-Host "==> source set to $ver" -ForegroundColor Cyan
}

function Build-Installer([string]$ver) {
  Write-Host "==> building $ver" -ForegroundColor Cyan
  make launcher-installer
  if ($LASTEXITCODE -ne 0) { Write-Error "make launcher-installer failed for $ver" }

  $stageDir = Join-Path $stageRoot $ver
  New-Item -ItemType Directory -Force -Path $stageDir | Out-Null

  $exe = "RLT-Launcher_${ver}_x64-setup.exe"
  $sig = "$exe.sig"
  Copy-Item -Force (Join-Path $bundleDir $exe) (Join-Path $stageDir $exe)
  Copy-Item -Force (Join-Path $bundleDir $sig) (Join-Path $stageDir $sig)
  Write-Host "    staged $stageDir\$exe" -ForegroundColor Gray
  Write-Host "    staged $stageDir\$sig" -ForegroundColor Gray
}

function Generate-Manifest([string]$ver) {
  $stageDir = Join-Path $stageRoot $ver
  $sigPath = Join-Path $stageDir "RLT-Launcher_${ver}_x64-setup.exe.sig"
  $url = "https://github.com/$owner/RLToolkit/releases/download/v$ver/RLT-Launcher_${ver}_x64-setup.exe"
  $out = Join-Path $stageDir "latest.json"

  Write-Host "==> generating $out" -ForegroundColor Cyan
  go run ./backend/cmd/gen-update-manifest `
    -version $ver `
    -sig $sigPath `
    -url $url `
    -notes "Auto-update smoke test" `
    -out $out
  if ($LASTEXITCODE -ne 0) { Write-Error "gen-update-manifest failed" }
  Write-Host "    staged $out" -ForegroundColor Gray
}

# ---- main ------------------------------------------------------------

# Make sure we start at 0.1.0 (the branch's current state).
Set-Version "0.1.0"
Build-Installer "0.1.0"

Set-Version "0.2.0"
Build-Installer "0.2.0"
Generate-Manifest "0.2.0"

Set-Version "0.1.0"

Write-Host ""
Write-Host "Done." -ForegroundColor Green
Write-Host ""
Write-Host "Upload to v0.1.0 GitHub release:"
Write-Host "  $stageRoot\0.1.0\RLT-Launcher_0.1.0_x64-setup.exe"
Write-Host "  $stageRoot\0.1.0\RLT-Launcher_0.1.0_x64-setup.exe.sig"
Write-Host ""
Write-Host "Upload to v0.2.0 GitHub release:"
Write-Host "  $stageRoot\0.2.0\RLT-Launcher_0.2.0_x64-setup.exe"
Write-Host "  $stageRoot\0.2.0\RLT-Launcher_0.2.0_x64-setup.exe.sig"
Write-Host "  $stageRoot\0.2.0\latest.json"
Write-Host ""
Write-Host "Source is back at 0.1.0 (no commits made)."
