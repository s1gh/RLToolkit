BINARY      := rl-toolkit
WIDGET_BIN  := rl-widget
LAUNCHER    := RLT-Launcher

GO_FLAGS    := -trimpath
LD_FLAGS    := -s -w
# Embed the build version into rl-toolkit so the plugin catalog can
# enforce min_launcher_version. Pre-release / dev builds compile with
# whatever VERSION is set in the environment; release targets pass it
# explicitly (see make release-linux / release-windows).
ifneq ($(VERSION),)
LD_FLAGS    += -X main.Version=$(VERSION)
endif

BIOME       := ./node_modules/.bin/biome

RELEASE_DIR  := release
TAURI_DIR    := overlay/src-tauri
TAURI_TARGET := $(TAURI_DIR)/target/release

# Host-only build: Tauri's webview links platform-specific system libs
# (webkit2gtk on Linux, WebView2 on Windows, WebKit on macOS), so
# cross-compiling the widget needs a per-target sysroot. The Go side
# stays aligned so backend + launcher ship as a matching pair.
ifeq ($(OS),Windows_NT)
	HOST_OS  := windows
	GOOS_VAL := windows
	EXE      := .exe
	MKDIR    = if not exist "$(subst /,\,$1)" mkdir "$(subst /,\,$1)"
	RM_RF    = if exist "$(subst /,\,$1)" rmdir /s /q "$(subst /,\,$1)"
	RM_F     = if exist "$(subst /,\,$1)" del /q "$(subst /,\,$1)"
	CP       = copy /y "$(subst /,\,$1)" "$(subst /,\,$2)" >nul
	SEP      := $(strip \)
else
	HOST_OS  := $(shell uname -s | tr '[:upper:]' '[:lower:]')
	GOOS_VAL := $(HOST_OS)
	EXE      :=
	MKDIR    = mkdir -p $1
	RM_RF    = rm -rf $1
	RM_F     = rm -f $1
	CP       = cp $1 $2
	SEP      := /
endif

OUT_DIR := $(RELEASE_DIR)/$(HOST_OS)

.PHONY: all backend widget launcher launcher-portable launcher-installer \
        webview2-runtime release release-windows release-linux run clean \
        release-clean fmt fmt-check lint check sdk

# --- SDK bundle: esbuild the modular sources into sdk/dist/sdk.js ------
#
# The Go binary embeds sdk/dist/sdk.js. The banner/footer wraps the
# IIFE in `if (!window.RLT) { … }` so a duplicate <script src="/sdk.js">
# include is a no-op.

SDK_OUT   := backend/internal/server/web/sdk/dist/sdk.js
SDK_ENTRY := backend/internal/server/web/sdk/src/index.js
SDK_BANNER := if(!window.RLT){
SDK_FOOTER := }

ifeq ($(HOST_OS),windows)
sdk:
	@$(call MKDIR,backend/internal/server/web/sdk/dist)
	npx esbuild $(SDK_ENTRY) --bundle --format=iife --target=es2020 --legal-comments=inline --sourcemap=external --banner:js="$(SDK_BANNER)" --footer:js="$(SDK_FOOTER)" --outfile=$(SDK_OUT)
else
sdk:
	@$(call MKDIR,backend/internal/server/web/sdk/dist)
	npx esbuild $(SDK_ENTRY) --bundle --format=iife --target=es2020 \
		--legal-comments=inline --sourcemap=external \
		--banner:js="$(SDK_BANNER)" --footer:js="$(SDK_FOOTER)" \
		--outfile=$(SDK_OUT)
endif

all: release

# --- backend: Go server / plugin host -----------------------------------

ifeq ($(HOST_OS),windows)
backend: sdk
	@$(call MKDIR,$(OUT_DIR))
	set "CGO_ENABLED=0" && set "GOOS=$(GOOS_VAL)" && set "GOARCH=amd64" && go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" -o $(OUT_DIR)/$(BINARY)$(EXE) ./backend/cmd/rl-toolkit
	@echo -- $(OUT_DIR)/$(BINARY)$(EXE)
else
backend: sdk
	@$(call MKDIR,$(OUT_DIR))
	CGO_ENABLED=0 GOOS=$(GOOS_VAL) GOARCH=amd64 \
		go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" \
		-o $(OUT_DIR)/$(BINARY)$(EXE) ./backend/cmd/rl-toolkit
	@echo "→ $(OUT_DIR)/$(BINARY)$(EXE)"
endif

# --- widget: standalone Tauri overlay binary ---------------------------

ifeq ($(HOST_OS),windows)
widget:
	@$(call MKDIR,$(OUT_DIR))
	cd $(subst /,\,$(TAURI_DIR)) && cargo build --release --features tauri/custom-protocol
	$(call CP,$(TAURI_TARGET)/$(WIDGET_BIN)$(EXE),$(OUT_DIR)/$(WIDGET_BIN)$(EXE))
	@echo -- $(OUT_DIR)/$(WIDGET_BIN)$(EXE)
else
widget:
	@$(call MKDIR,$(OUT_DIR))
	cd $(TAURI_DIR) && cargo build --release --features tauri/custom-protocol
	$(call CP,$(TAURI_TARGET)/$(WIDGET_BIN)$(EXE),$(OUT_DIR)/$(WIDGET_BIN)$(EXE))
	@echo "→ $(OUT_DIR)/$(WIDGET_BIN)$(EXE)"
endif

# --- launcher-portable: standalone launcher exe (no installer) ---------
#
# --no-bundle produces just the exe; the rl-toolkit sidecar lands next
# to it in target/release/. Both are copied to OUT_DIR so the launcher
# can locate the sidecar at runtime. The binary is renamed from
# rl-widget to RLT-Launcher to match the productName.

ifeq ($(HOST_OS),windows)
launcher-portable: sdk
	@$(call MKDIR,$(OUT_DIR))
	@for /f "tokens=2" %%i in ('rustc -vV ^| findstr /B "host:"') do @( \
	  echo host triple: %%i && \
	  go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" \
	    -o $(TAURI_DIR)/binaries/rl-toolkit-%%i.exe ./backend/cmd/rl-toolkit && \
	  cd $(subst /,\,$(TAURI_DIR)) && \
	  cargo tauri build --no-bundle --config tauri.launcher-portable.json \
	)
	$(call CP,$(TAURI_TARGET)/$(WIDGET_BIN)$(EXE),$(OUT_DIR)/$(LAUNCHER)$(EXE))
	$(call CP,$(TAURI_TARGET)/$(BINARY)$(EXE),$(OUT_DIR)/$(BINARY)$(EXE))
	@echo -- $(OUT_DIR)/$(LAUNCHER)$(EXE)
	@echo -- $(OUT_DIR)/$(BINARY)$(EXE) (sidecar)
else
launcher-portable: sdk
	@$(call MKDIR,$(OUT_DIR))
	@triple=$$(rustc -vV | sed -n 's/host: //p'); \
	  echo "host triple: $$triple"; \
	  go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" \
	    -o $(TAURI_DIR)/binaries/rl-toolkit-$$triple ./backend/cmd/rl-toolkit && \
	  cd $(TAURI_DIR) && \
	  cargo tauri build --no-bundle --config tauri.launcher-portable.json
	$(call CP,$(TAURI_TARGET)/$(WIDGET_BIN)$(EXE),$(OUT_DIR)/$(LAUNCHER)$(EXE))
	$(call CP,$(TAURI_TARGET)/$(BINARY)$(EXE),$(OUT_DIR)/$(BINARY)$(EXE))
	@echo "→ $(OUT_DIR)/$(LAUNCHER)$(EXE)"
	@echo "→ $(OUT_DIR)/$(BINARY)$(EXE) (sidecar)"
endif

# --- webview2-runtime: fetch + extract the fixed-version runtime ----
#
# Tauri's bundle.windows.webviewInstallMode = fixedRuntime points at
# overlay/src-tauri/webview2-runtime/ and ships it inside the NSIS
# installer. We pin to 143.0.3650.139, the last release before the
# WebView2 144+ ghost-titlebar regression on transparent windows
# (tauri-apps/tauri#14764). The current evergreen runtime (148.x as of
# this writing) still ships the bug, and Microsoft's fix has not
# reached stable despite earlier claims.
#
# Source: NuGet's WebView2.Runtime.X64 package, which carries the
# fixed-version distribution. The .nupkg is a zip; we extract the
# contentFiles/any/any/WebView2/ subtree, which matches the layout
# of Microsoft's Fixed Version CAB. ~250 MB on disk, English-only
# locale (the NSIS bundle is also English-only, so no mismatch).
#
# Idempotent: if the runtime folder is already present and
# non-empty, skip the fetch. Set WEBVIEW2_FORCE=1 to re-download.

WEBVIEW2_VERSION := 143.0.3650.139
WEBVIEW2_DIR     := $(TAURI_DIR)/webview2-runtime
WEBVIEW2_NUPKG   := $(TAURI_DIR)/webview2-runtime.nupkg
WEBVIEW2_URL     := https://api.nuget.org/v3-flatcontainer/webview2.runtime.x64/$(WEBVIEW2_VERSION)/webview2.runtime.x64.$(WEBVIEW2_VERSION).nupkg

ifeq ($(HOST_OS),windows)
.PHONY: webview2-runtime
webview2-runtime:
	@powershell -NoProfile -ExecutionPolicy Bypass -Command "$$ErrorActionPreference='Stop'; \
	  $$dir = '$(subst /,\,$(WEBVIEW2_DIR))'; \
	  $$pkg = '$(subst /,\,$(WEBVIEW2_NUPKG))'; \
	  $$url = '$(WEBVIEW2_URL)'; \
	  if ((Test-Path $$dir) -and ((Get-ChildItem $$dir | Measure-Object).Count -gt 0) -and (-not $$env:WEBVIEW2_FORCE)) { \
	    Write-Host \"WebView2 runtime $(WEBVIEW2_VERSION) already present in $$dir; skipping fetch (set WEBVIEW2_FORCE=1 to re-download)\"; exit 0 \
	  }; \
	  Write-Host \"Fetching WebView2 runtime $(WEBVIEW2_VERSION) from NuGet (~250 MB)...\"; \
	  Invoke-WebRequest -Uri $$url -OutFile $$pkg; \
	  if (Test-Path $$dir) { Remove-Item -Recurse -Force $$dir }; \
	  $$tmp = Join-Path $$env:TEMP ('wv2-' + [Guid]::NewGuid().ToString('N')); \
	  Expand-Archive -Path $$pkg -DestinationPath $$tmp -Force; \
	  Move-Item -Path (Join-Path $$tmp 'contentFiles\\any\\any\\WebView2') -Destination $$dir; \
	  Remove-Item -Recurse -Force $$tmp; \
	  Remove-Item -Force $$pkg; \
	  Write-Host \"WebView2 runtime extracted to $$dir\""
else
.PHONY: webview2-runtime
webview2-runtime:
	@echo "webview2-runtime is Windows-only — run on a Windows host" && false
endif

# --- launcher-installer (Windows only): NSIS installer with updater --
#
# Requires TAURI_SIGNING_PRIVATE_KEY[_PASSWORD] for the updater .sig.
# Depends on webview2-runtime: the fixed-version WebView2 runtime is
# bundled into the installer (see tauri.launcher.json → webviewInstall
# Mode = fixedRuntime).

ifeq ($(HOST_OS),windows)
launcher-installer: sdk webview2-runtime
	@$(call MKDIR,$(OUT_DIR))
	@for /f "tokens=2" %%i in ('rustc -vV ^| findstr /B "host:"') do @( \
	  echo host triple: %%i && \
	  go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" \
	    -o $(TAURI_DIR)/binaries/rl-toolkit-%%i.exe ./backend/cmd/rl-toolkit && \
	  cd $(subst /,\,$(TAURI_DIR)) && \
	  cargo tauri build --features bundled-updater --config tauri.launcher.json \
	)
	@echo NSIS bundle: $(TAURI_TARGET)\bundle\nsis
else
launcher-installer:
	@echo "launcher-installer is Windows-only — run on a Windows host" && false
endif

# --- launcher: alias for launcher-portable ----------------------------
launcher: launcher-portable

# --- release-windows: Windows artifacts only -------------------------
#
# Produces the three files for the Windows side of a release:
#   release/windows/RLToolkit_<v>_x64-setup.exe       (NSIS, signed)
#   release/windows/RLToolkit_<v>_x64-setup.exe.sig
#   release/windows/RLToolkit_<v>_x64-portable.zip
#
# latest.json is generated by the Linux CI job after the AppImage is
# built, so a single multi-platform manifest covers both sides.
#
# Inputs (env or make var):
#   VERSION              required; e.g. 0.2.0 (no leading v)
#   RELEASE_OWNER        required; GitHub owner used in URLs
#   TAURI_SIGNING_PRIVATE_KEY[_PASSWORD]  required for signing

ifeq ($(HOST_OS),windows)
.PHONY: release-windows
release-windows: launcher-installer launcher-portable
	@if "$(VERSION)"=="" ( echo VERSION required & exit 1 )
	@if "$(RELEASE_OWNER)"=="" ( echo RELEASE_OWNER required & exit 1 )
	@$(call MKDIR,$(OUT_DIR))
	@copy /y "$(subst /,\,$(TAURI_TARGET))\bundle\nsis\RLT-Launcher_$(VERSION)_x64-setup.exe" "$(subst /,\,$(OUT_DIR))\RLToolkit_$(VERSION)_x64-setup.exe" >nul
	@copy /y "$(subst /,\,$(TAURI_TARGET))\bundle\nsis\RLT-Launcher_$(VERSION)_x64-setup.exe.sig" "$(subst /,\,$(OUT_DIR))\RLToolkit_$(VERSION)_x64-setup.exe.sig" >nul
	@powershell -NoProfile -Command "Compress-Archive -Force -Path '$(subst /,\,$(OUT_DIR))\$(LAUNCHER)$(EXE)','$(subst /,\,$(OUT_DIR))\$(BINARY)$(EXE)' -DestinationPath '$(subst /,\,$(OUT_DIR))\RLToolkit_$(VERSION)_x64-portable.zip'"
	@echo Release artefacts in $(OUT_DIR):
	@dir /b $(subst /,\,$(OUT_DIR))
else
.PHONY: release-windows
release-windows:
	@echo "release-windows is Windows-only — run on a Windows host" && false
endif

# --- release-linux: Linux artifacts only -----------------------------
#
# Produces the three files for the Linux side of a release:
#   release/linux/RLToolkit_<v>_x86_64.AppImage           (signed)
#   release/linux/RLToolkit_<v>_x86_64.AppImage.sig
#   release/linux/RLToolkit_<v>_x86_64-portable.tar.gz
#
# The CI workflow combines this build's .sig with the Windows .sig
# (downloaded from the release) into a single multi-platform manifest.
#
# Inputs (env or make var):
#   VERSION              required; e.g. 0.2.0 (no leading v)
#   RELEASE_OWNER        required; GitHub owner used in URLs
#   TAURI_SIGNING_PRIVATE_KEY[_PASSWORD]  required for signing

ifeq ($(HOST_OS),linux)
.PHONY: release-linux
release-linux: sdk
	@if [ -z "$(VERSION)" ]; then echo "VERSION required"; exit 1; fi
	@if [ -z "$(RELEASE_OWNER)" ]; then echo "RELEASE_OWNER required"; exit 1; fi
	@$(call MKDIR,$(OUT_DIR))
	@triple=$$(rustc -vV | sed -n 's/host: //p'); \
	  echo "host triple: $$triple"; \
	  go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" \
	    -o $(TAURI_DIR)/binaries/$(BINARY)-$$triple ./backend/cmd/rl-toolkit && \
	  cd $(TAURI_DIR) && \
	  NO_STRIP=1 cargo tauri build --features bundled-updater --config tauri.launcher.json
	@# Patch + re-sign: linuxdeploy-plugin-gtk's AppRun hook hard-codes
	@# GDK_BACKEND=x11, which kills overlay transparency on Wayland
	@# (Hyprland layer-shell especially). Extract, neuter the hook,
	@# drop the wayland + epoxy libs that linuxdeploy bundles from the
	@# build host, repack with appimagetool, then re-sign with
	@# `tauri signer sign`.
	@#
	@# Why drop the wayland libs: linuxdeploy doesn't bundle Mesa, so
	@# libEGL/libGL/libgbm always come from the user's host. On hosts
	@# with newer wayland (e.g. Arch/CachyOS at wayland 1.25), host
	@# Mesa drives a Wayland EGL display through the OLDER bundled
	@# libwayland-client and fails with EGL_BAD_PARAMETER → blank
	@# launcher window. Letting wayland-client resolve from the host
	@# pairs it with host Mesa and the EGL init succeeds.
	@#
	@# The rest of the GTK / cairo / pango / glib stack stays bundled.
	@# Earlier we tried stripping it on top of Ubuntu 22.04 builds to
	@# fix vertical-line artifacts on transparent-window resize, but
	@# the actual cause was bundled GTK 3.24.33 specifically — a
	@# Wayland surface bug that's fixed in 3.24.41 (Ubuntu 24.04). We
	@# now build on 24.04, so bundled GTK is no longer the problem and
	@# the broad strip isn't needed. libgstgl stays bundled — host
	@# gstreamer ABI tends to drift the other way.
	@#
	@# appimagetool is taken from Tauri's own cache (populated during
	@# `cargo tauri build`), with a host-installed binary as fallback.
	@tmp=$$(mktemp -d); \
	  cp -f $(TAURI_TARGET)/bundle/appimage/$(LAUNCHER)_$(VERSION)_amd64.AppImage "$$tmp/in.AppImage"; \
	  chmod +x "$$tmp/in.AppImage"; \
	  cd "$$tmp" && ./in.AppImage --appimage-extract > /dev/null && cd - > /dev/null; \
	  printf '#! /usr/bin/env bash\n# Hook neutered: original forced GDK_BACKEND=x11 which breaks\n# Wayland transparency / layer-shell. Bundled libs init fine\n# without env tweaks (host backend auto-detected by GDK).\n:\n' \
	    > "$$tmp/squashfs-root/apprun-hooks/linuxdeploy-plugin-gtk.sh"; \
	  chmod +x "$$tmp/squashfs-root/apprun-hooks/linuxdeploy-plugin-gtk.sh"; \
	  for lib in libwayland-client.so.0 libwayland-cursor.so.0 libwayland-egl.so.1 libwayland-server.so.0 libepoxy.so.0; do \
	    rm -f "$$tmp/squashfs-root/usr/lib/$$lib" "$$tmp/squashfs-root/usr/lib/x86_64-linux-gnu/$$lib"; \
	  done; \
	  appimagetool=$$(find /tmp -maxdepth 4 -path '*/appimage_extracted_*/usr/bin/appimagetool' 2>/dev/null | head -1); \
	  if [ -z "$$appimagetool" ]; then appimagetool=$$(command -v appimagetool); fi; \
	  if [ -z "$$appimagetool" ]; then echo "appimagetool not found; install appimagetool-bin or run cargo tauri build first to populate Tauri's cache" && exit 1; fi; \
	  echo "using appimagetool: $$appimagetool"; \
	  ARCH=x86_64 "$$appimagetool" "$$tmp/squashfs-root" "$(OUT_DIR)/RLToolkit_$(VERSION)_x86_64.AppImage" 2>&1 | tail -3; \
	  chmod +x "$(OUT_DIR)/RLToolkit_$(VERSION)_x86_64.AppImage"; \
	  rm -rf "$$tmp"
	@cargo tauri signer sign "$(OUT_DIR)/RLToolkit_$(VERSION)_x86_64.AppImage" > /dev/null
	@# Portable tarball: stage the launcher (renamed rl-widget →
	@# RLT-Launcher, matching the Windows convention) and the
	@# rl-toolkit sidecar, then tar.
	@tmp=$$(mktemp -d); \
	  staged="$$tmp/RLToolkit_$(VERSION)_x86_64-portable"; \
	  mkdir -p "$$staged"; \
	  cp -f $(TAURI_TARGET)/$(WIDGET_BIN) "$$staged/$(LAUNCHER)"; \
	  cp -f $(TAURI_TARGET)/$(BINARY)     "$$staged/$(BINARY)"; \
	  chmod +x "$$staged/$(LAUNCHER)" "$$staged/$(BINARY)"; \
	  tar -C "$$tmp" -czf $(OUT_DIR)/RLToolkit_$(VERSION)_x86_64-portable.tar.gz \
	    "RLToolkit_$(VERSION)_x86_64-portable"; \
	  rm -rf "$$tmp"
	@echo "Release artefacts in $(OUT_DIR):"
	@ls -1 $(OUT_DIR)
else
.PHONY: release-linux
release-linux:
	@echo "release-linux is Linux-only — run on a Linux host (or in CI)" && false
endif

# --- release: full stack (backend + launcher) ------------------------

ifeq ($(HOST_OS),windows)
release: backend launcher
	@$(call MKDIR,$(OUT_DIR))
	@echo.
	@echo Release artefacts under $(OUT_DIR)/:
	@dir /b $(subst /,\,$(OUT_DIR))
else
release: backend launcher
	@$(call MKDIR,$(OUT_DIR))
	@echo ""
	@echo "Release artefacts under $(OUT_DIR)/:"
	@find $(OUT_DIR) -maxdepth 1 | sort
endif

release-clean:
	$(call RM_RF,$(RELEASE_DIR))

# --- run / clean --------------------------------------------------------

ifeq ($(HOST_OS),windows)
run: backend
	$(subst /,\,$(OUT_DIR))\$(BINARY)$(EXE)
else
run: backend
	./$(OUT_DIR)/$(BINARY)$(EXE)
endif

clean:
	$(call RM_F,$(BINARY))
	$(call RM_F,$(BINARY).exe)
	$(call RM_RF,$(RELEASE_DIR))

# --- formatting / linting ----------------------------------------------

fmt:
	$(BIOME) format --write .

fmt-check:
	$(BIOME) format .

lint:
	$(BIOME) lint .

check:
	$(BIOME) check .

# --- bump: rewrite version in Cargo.toml + tauri.conf.json -------------
#
# Usage:
#   make bump VERSION=0.3.0
#
# Rewrites the version line in overlay/src-tauri/Cargo.toml and
# overlay/src-tauri/tauri.conf.json. Does NOT commit, push, or tag --
# inspect the diff, then commit manually:
#
#   git diff overlay/src-tauri/
#   git commit -am "chore: bump to 0.3.0"
#   git push
#   gh workflow run release.yml -f tag=v0.3.0
#
# VERSION must be strict X.Y.Z (no leading v, no pre-release suffix).
# The Tauri updater and latest.json both compare versions semver-style;
# stick to that format to avoid surprises in the upgrade flow.
#
# Idempotent: running twice with the same VERSION is a no-op on the
# second run.

CARGO_TOML  := overlay/src-tauri/Cargo.toml
TAURI_CONF  := overlay/src-tauri/tauri.conf.json

.PHONY: bump
ifeq ($(HOST_OS),windows)
bump:
	@if "$(VERSION)"=="" ( echo VERSION required, e.g. make bump VERSION=0.3.0 & exit 1 )
	@powershell -NoProfile -Command "if ('$(VERSION)' -notmatch '^[0-9]+\.[0-9]+\.[0-9]+$$') { Write-Error 'VERSION must be strict semver X.Y.Z (no leading v, no suffix)'; exit 1 }"
	@powershell -NoProfile -Command "$$v='$(VERSION)'; $$utf8=New-Object System.Text.UTF8Encoding $$false; $$p=Resolve-Path '$(CARGO_TOML)'; $$c=Get-Content $$p -Raw; $$c=[regex]::Replace($$c, '(?m)^version = \"[^\"]*\"$$', \"version = `\"$$v`\"\", 1); [System.IO.File]::WriteAllText($$p, $$c, $$utf8)"
	@powershell -NoProfile -Command "$$v='$(VERSION)'; $$utf8=New-Object System.Text.UTF8Encoding $$false; $$p=Resolve-Path '$(TAURI_CONF)'; $$t=Get-Content $$p -Raw; $$t=[regex]::Replace($$t, '(\"version\":\s*\")[^\"]*(\")', \"`$${1}$$v`$${2}\"); [System.IO.File]::WriteAllText($$p, $$t, $$utf8)"
	@echo Bumped to $(VERSION):
	@powershell -NoProfile -Command "Select-String -Path '$(CARGO_TOML)' -Pattern '^version' | Select-Object -First 1"
	@powershell -NoProfile -Command "Select-String -Path '$(TAURI_CONF)' -Pattern '\"version\":'"
else
bump:
	@if [ -z "$(VERSION)" ]; then echo "VERSION required, e.g. make bump VERSION=0.3.0"; exit 1; fi
	@echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { \
	  echo "VERSION must be strict semver X.Y.Z (no leading v, no suffix)"; exit 1; }
	@sed -i -E '0,/^version = "[^"]*"$$/s//version = "$(VERSION)"/' $(CARGO_TOML)
	@sed -i -E 's/("version": ")[^"]*(")/\1$(VERSION)\2/' $(TAURI_CONF)
	@echo "Bumped to $(VERSION):"
	@grep '^version' $(CARGO_TOML) | head -1
	@grep '"version":' $(TAURI_CONF)
endif
