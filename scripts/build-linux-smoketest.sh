#!/usr/bin/env bash
# Build both 0.1.0 and 0.2.0 AppImages in one go for the auto-update
# smoke test. Run from the repo root on a Linux host. Requires:
#
#   $TAURI_SIGNING_PRIVATE_KEY              the .key file contents (or path; we use contents)
#   $TAURI_SIGNING_PRIVATE_KEY_PASSWORD     the matching password
#   $RELEASE_OWNER                          GitHub owner; default s1gh
#
# What it does (in order):
#   1. Build 0.1.0  -> stage in dist/smoketest/0.1.0/
#   2. Bump version files in place -> 0.2.0
#   3. Build 0.2.0  -> stage in dist/smoketest/0.2.0/
#   4. Generate Linux-only latest.json (using gen-update-manifest's
#      -linux-sig/-url pair only — the Windows pair is omitted, which
#      is supported and produces a manifest with just one platform key)
#   5. Revert version files back to 0.1.0
#
# After it finishes, you have:
#   dist/smoketest/0.1.0/RLToolkit_0.1.0_x86_64.AppImage (+ .sig)
#   dist/smoketest/0.2.0/RLToolkit_0.2.0_x86_64.AppImage (+ .sig + latest.json)

set -euo pipefail

if [[ -z "${TAURI_SIGNING_PRIVATE_KEY:-}" || -z "${TAURI_SIGNING_PRIVATE_KEY_PASSWORD:-}" ]]; then
  echo "Set TAURI_SIGNING_PRIVATE_KEY and TAURI_SIGNING_PRIVATE_KEY_PASSWORD before running." >&2
  exit 1
fi

OWNER="${RELEASE_OWNER:-s1gh}"
CARGO_TOML="overlay/src-tauri/Cargo.toml"
TAURI_CONF="overlay/src-tauri/tauri.conf.json"
BUNDLE_DIR="overlay/src-tauri/target/release/bundle/appimage"
STAGE_ROOT="dist/smoketest"

set_version() {
  local ver="$1"
  # Cargo.toml: only the [package] version line, not [dependencies] versions.
  sed -i -E "0,/^version = \".*\"$/s//version = \"${ver}\"/" "$CARGO_TOML"
  # tauri.conf.json: the top-level "version": "..." field.
  sed -i -E "s/(\"version\": \")[^\"]*(\")/\1${ver}\2/" "$TAURI_CONF"
  echo "==> source set to ${ver}"
}

build_appimage() {
  local ver="$1"
  echo "==> building ${ver}"
  make release-linux VERSION="${ver}" RELEASE_OWNER="${OWNER}"

  local stage="${STAGE_ROOT}/${ver}"
  mkdir -p "${stage}"

  cp -f "release/linux/RLToolkit_${ver}_x86_64.AppImage"     "${stage}/"
  cp -f "release/linux/RLToolkit_${ver}_x86_64.AppImage.sig" "${stage}/"
  echo "    staged ${stage}/RLToolkit_${ver}_x86_64.AppImage"
  echo "    staged ${stage}/RLToolkit_${ver}_x86_64.AppImage.sig"
}

generate_manifest() {
  local ver="$1"
  local stage="${STAGE_ROOT}/${ver}"
  local sig="${stage}/RLToolkit_${ver}_x86_64.AppImage.sig"
  local url="https://github.com/${OWNER}/RLToolkit/releases/download/v${ver}/RLToolkit_${ver}_x86_64.AppImage"
  local out="${stage}/latest.json"

  echo "==> generating ${out}"
  go run ./backend/cmd/gen-update-manifest \
    -version "${ver}" \
    -linux-sig "${sig}" \
    -linux-url "${url}" \
    -notes "Auto-update smoke test" \
    -out "${out}"
  echo "    staged ${out}"
}

set_version "0.1.0"
build_appimage "0.1.0"

set_version "0.2.0"
build_appimage "0.2.0"
generate_manifest "0.2.0"

set_version "0.1.0"

echo
echo "Done."
echo
echo "Upload to v0.1.0 GitHub release:"
echo "  ${STAGE_ROOT}/0.1.0/RLToolkit_0.1.0_x86_64.AppImage"
echo "  ${STAGE_ROOT}/0.1.0/RLToolkit_0.1.0_x86_64.AppImage.sig"
echo
echo "Upload to v0.2.0 GitHub release:"
echo "  ${STAGE_ROOT}/0.2.0/RLToolkit_0.2.0_x86_64.AppImage"
echo "  ${STAGE_ROOT}/0.2.0/RLToolkit_0.2.0_x86_64.AppImage.sig"
echo "  ${STAGE_ROOT}/0.2.0/latest.json"
echo
echo "Source is back at 0.1.0 (no commits made)."
