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
else
	HOST_OS  := $(shell uname -s | tr '[:upper:]' '[:lower:]')
	GOOS_VAL := $(HOST_OS)
	EXE      :=
endif

OUT_DIR := $(RELEASE_DIR)/$(HOST_OS)

.PHONY: all backend widget launcher release run clean release-clean \
        fmt fmt-check lint check

# Default: full stack (backend + launcher, which also yields the widget).
all: release

# --- backend: Go server / plugin host -----------------------------------

backend:
	@mkdir -p $(OUT_DIR)
	cd backend && CGO_ENABLED=0 GOOS=$(GOOS_VAL) GOARCH=amd64 \
		go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" \
		-o ../$(OUT_DIR)/$(BINARY)$(EXE) .
	@echo "→ $(OUT_DIR)/$(BINARY)$(EXE)"

# --- widget: standalone Tauri overlay binary ---------------------------

widget:
	@mkdir -p $(OUT_DIR)
	cd $(TAURI_DIR) && cargo build --release --features tauri/custom-protocol
	cp $(TAURI_TARGET)/$(WIDGET_BIN)$(EXE) $(OUT_DIR)/$(WIDGET_BIN)$(EXE)
	@echo "→ $(OUT_DIR)/$(WIDGET_BIN)$(EXE)"

# --- launcher: combined installer (Tauri bundle + Go sidecar) ----------
#
# Tauri's [[bin]] name (rl-widget) is the executable filename; the
# productName override in tauri.launcher.json only renames the bundles
# (.deb / .rpm / .AppImage / .msi). We copy the binary out as
# RLT-Launcher and the bundles stay under target/release/bundle/.

ifeq ($(HOST_OS),windows)
launcher:
	@mkdir -p $(OUT_DIR)
	@for /f "tokens=2" %%i in ('rustc -vV ^| findstr /B "host:"') do @( \
	  echo host triple: %%i && \
	  cd backend && go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" \
	    -o ../$(TAURI_DIR)/binaries/rl-toolkit-%%i.exe . && \
	  cd ../$(TAURI_DIR) && \
	  cargo tauri build --no-bundle --config tauri.launcher.json \
	)
	cp $(TAURI_TARGET)/$(WIDGET_BIN)$(EXE) $(OUT_DIR)/$(LAUNCHER)$(EXE)
	cp $(TAURI_TARGET)/$(WIDGET_BIN)$(EXE) $(OUT_DIR)/$(WIDGET_BIN)$(EXE)
	@echo "→ $(OUT_DIR)/$(LAUNCHER)$(EXE)"
	@echo "→ $(OUT_DIR)/$(WIDGET_BIN)$(EXE)"
else
launcher:
	@mkdir -p $(OUT_DIR)
	@triple=$$(rustc -vV | sed -n 's/host: //p'); \
	  echo "host triple: $$triple"; \
	  cd backend && go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" \
	    -o ../$(TAURI_DIR)/binaries/rl-toolkit-$$triple . && \
	  cd ../$(TAURI_DIR) && \
	  cargo tauri build --no-bundle --config tauri.launcher.json
	cp $(TAURI_TARGET)/$(WIDGET_BIN)$(EXE) $(OUT_DIR)/$(LAUNCHER)$(EXE)
	cp $(TAURI_TARGET)/$(WIDGET_BIN)$(EXE) $(OUT_DIR)/$(WIDGET_BIN)$(EXE)
	@echo "→ $(OUT_DIR)/$(LAUNCHER)$(EXE)"
	@echo "→ $(OUT_DIR)/$(WIDGET_BIN)$(EXE)"
endif

# --- release: full stack (backend + launcher, launcher emits widget too)

release: backend launcher
	@mkdir -p $(OUT_DIR)
	rm -rf $(OUT_DIR)/plugins $(OUT_DIR)/data
	cp -r plugins $(OUT_DIR)/plugins
	cp -r data $(OUT_DIR)/data
	@echo ""
	@echo "Release artefacts under $(OUT_DIR)/:"
	@find $(OUT_DIR) -maxdepth 1 | sort

release-clean:
	rm -rf $(RELEASE_DIR)

# --- run / clean --------------------------------------------------------

run: backend
	./$(OUT_DIR)/$(BINARY)$(EXE)

clean:
	rm -f $(BINARY) $(BINARY).exe
	rm -rf $(RELEASE_DIR)

# --- formatting / linting ----------------------------------------------

fmt:
	$(BIOME) format --write .

fmt-check:
	$(BIOME) format .

lint:
	$(BIOME) lint .

check:
	$(BIOME) check .
