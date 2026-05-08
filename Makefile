BINARY      := rl-toolkit
WIDGET_BIN  := rl-widget
LAUNCHER    := RLT-Launcher

GO_FLAGS    := -trimpath
LD_FLAGS    := -s -w

BIOME       := ./node_modules/.bin/biome

RELEASE_DIR  := release
TAURI_DIR    := overlay/src-tauri
TAURI_TARGET := $(TAURI_DIR)/target/release

# Host OS detection. We build for the host only — wry can't cross-
# compile the widget, so we keep the Go side aligned.
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
        release release-windows run clean release-clean \
        fmt fmt-check lint check sdk

# --- SDK bundle: esbuild the modular sources into sdk/dist/sdk.js ------
#
# The Go binary embeds sdk/dist/sdk.js. Sources live under
# backend/internal/server/web/sdk/src/. Source maps land alongside the
# bundle (--sourcemap=external) so production stack traces resolve.
#
# The banner/footer wraps the entire bundled IIFE in `if (!window.RLT)
# { … }` so a duplicate <script src="/sdk.js"> include is a no-op
# (matches the legacy monolith's `if (window.RLT) return` guard).
# Module-top-level side effects (manifest fetch, identity bootHydrate,
# bus subscriptions) live inside the wrapped block.

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

# Default: full stack (backend + launcher, which also yields the widget).
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
# Tauri's [[bin]] name (rl-widget) is the executable filename; the
# productName override in the launcher configs renames the bundle. We
# copy the binary out as RLT-Launcher and the bundles stay under
# target/release/bundle/.
#
# --no-bundle produces just the exe; the sidecar (rl-toolkit) lands
# next to it in target/release/. Both are copied to OUT_DIR so the
# launcher can locate the sidecar at runtime.

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

# --- launcher-installer (Windows only): NSIS installer with updater --
#
# Requires TAURI_SIGNING_PRIVATE_KEY and TAURI_SIGNING_PRIVATE_KEY_PASSWORD
# env vars to produce a signed update. Without them Tauri builds the
# NSIS but skips the .sig file, which is useless for the updater.

ifeq ($(HOST_OS),windows)
launcher-installer: sdk
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
# Produces the three files needed to populate a draft GitHub release:
#   release/windows/RLToolkit_<v>_x64-setup.exe       (NSIS, signed)
#   release/windows/RLToolkit_<v>_x64-setup.exe.sig
#   release/windows/RLToolkit_<v>_x64-portable.zip
#
# Note: latest.json is NOT generated here. The Linux CI job (see
# .github/workflows/release-linux.yml) downloads the Windows .sig
# from the GitHub release and produces a single multi-platform
# manifest after the AppImage is built. This avoids two manifests
# competing on one release.
#
# Inputs (env or make var):
#   VERSION              required; e.g. 0.2.0 (no leading v)
#   RELEASE_OWNER        required; GitHub owner used in the URL hint below
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

# --- release: full stack (backend + launcher, launcher emits widget too)

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
