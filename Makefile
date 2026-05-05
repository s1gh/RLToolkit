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

.PHONY: all backend widget launcher release run clean release-clean \
        fmt fmt-check lint check

# Default: full stack (backend + launcher, which also yields the widget).
all: release

# --- backend: Go server / plugin host -----------------------------------

ifeq ($(HOST_OS),windows)
backend:
	@$(call MKDIR,$(OUT_DIR))
	cd backend && set "CGO_ENABLED=0" && set "GOOS=$(GOOS_VAL)" && set "GOARCH=amd64" && go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" -o ../$(OUT_DIR)/$(BINARY)$(EXE) .
	@echo -- $(OUT_DIR)/$(BINARY)$(EXE)
else
backend:
	@$(call MKDIR,$(OUT_DIR))
	cd backend && CGO_ENABLED=0 GOOS=$(GOOS_VAL) GOARCH=amd64 \
		go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" \
		-o ../$(OUT_DIR)/$(BINARY)$(EXE) .
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

# --- launcher: combined installer (Tauri bundle + Go sidecar) ----------
#
# Tauri's [[bin]] name (rl-widget) is the executable filename; the
# productName override in tauri.launcher.json only renames the bundles
# (.deb / .rpm / .AppImage / .msi). We copy the binary out as
# RLT-Launcher and the bundles stay under target/release/bundle/.
#
# --no-bundle produces just the exe; the sidecar (rl-toolkit) lands
# next to it in target/release/. Both are copied to OUT_DIR so the
# launcher can locate the sidecar at runtime.

ifeq ($(HOST_OS),windows)
launcher:
	@$(call MKDIR,$(OUT_DIR))
	@for /f "tokens=2" %%i in ('rustc -vV ^| findstr /B "host:"') do @( \
	  echo host triple: %%i && \
	  cd backend && go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" \
	    -o ../$(TAURI_DIR)/binaries/rl-toolkit-%%i.exe . && \
	  cd ../$(subst /,\,$(TAURI_DIR)) && \
	  cargo tauri build --no-bundle --config tauri.launcher.json \
	)
	$(call CP,$(TAURI_TARGET)/$(WIDGET_BIN)$(EXE),$(OUT_DIR)/$(LAUNCHER)$(EXE))
	$(call CP,$(TAURI_TARGET)/$(WIDGET_BIN)$(EXE),$(OUT_DIR)/$(WIDGET_BIN)$(EXE))
	$(call CP,$(TAURI_TARGET)/$(BINARY)$(EXE),$(OUT_DIR)/$(BINARY)$(EXE))
	@echo -- $(OUT_DIR)/$(LAUNCHER)$(EXE)
	@echo -- $(OUT_DIR)/$(WIDGET_BIN)$(EXE)
	@echo -- $(OUT_DIR)/$(BINARY)$(EXE) (sidecar)
else
launcher:
	@$(call MKDIR,$(OUT_DIR))
	@triple=$$(rustc -vV | sed -n 's/host: //p'); \
	  echo "host triple: $$triple"; \
	  cd backend && go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" \
	    -o ../$(TAURI_DIR)/binaries/rl-toolkit-$$triple . && \
	  cd ../$(TAURI_DIR) && \
	  cargo tauri build --no-bundle --config tauri.launcher.json
	$(call CP,$(TAURI_TARGET)/$(WIDGET_BIN)$(EXE),$(OUT_DIR)/$(LAUNCHER)$(EXE))
	$(call CP,$(TAURI_TARGET)/$(WIDGET_BIN)$(EXE),$(OUT_DIR)/$(WIDGET_BIN)$(EXE))
	$(call CP,$(TAURI_TARGET)/$(BINARY)$(EXE),$(OUT_DIR)/$(BINARY)$(EXE))
	@echo "→ $(OUT_DIR)/$(LAUNCHER)$(EXE)"
	@echo "→ $(OUT_DIR)/$(WIDGET_BIN)$(EXE)"
	@echo "→ $(OUT_DIR)/$(BINARY)$(EXE) (sidecar)"
endif

# --- release: full stack (backend + launcher, launcher emits widget too)

ifeq ($(HOST_OS),windows)
release: backend launcher
	@$(call MKDIR,$(OUT_DIR))
	@$(call MKDIR,$(OUT_DIR)/data)
	@echo.
	@echo Release artefacts under $(OUT_DIR)/:
	@dir /b $(subst /,\,$(OUT_DIR))
else
release: backend launcher
	@$(call MKDIR,$(OUT_DIR))
	@$(call MKDIR,$(OUT_DIR)/data)
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
