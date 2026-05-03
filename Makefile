BINARY      := rl-toolkit
GO_FLAGS    := -trimpath
LD_FLAGS    := -s -w

# Biome lives in node_modules/.bin after `npm install`. Keep the path
# explicit so `make fmt` / `make lint` works without requiring a global
# install or an active shell PATH.
BIOME       := ./node_modules/.bin/biome

RELEASE_DIR   := release
WIDGET_BIN    := rl-widget
TAURI_TARGET  := overlay/src-tauri/target/release

.PHONY: all linux windows clean run fmt fmt-check lint check release release-go release-widget release-clean

all: linux windows

linux:
	cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" -o ../$(BINARY) .

windows:
	cd backend && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" -o ../$(BINARY).exe .

# --- release: bundle Go backend (both OS) + host-OS widget into release/

# Cross-compile the Go backend for both Linux and Windows into release/.
release-go:
	mkdir -p $(RELEASE_DIR)/linux $(RELEASE_DIR)/windows
	cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" -o ../$(RELEASE_DIR)/linux/$(BINARY) .
	cd backend && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" -o ../$(RELEASE_DIR)/windows/$(BINARY).exe .

# Build the Tauri overlay natively for the host OS. wry can't cross-compile,
# so the widget for the OTHER OS must be built from a host running that OS.
release-widget:
	cd overlay/src-tauri && cargo build --release
	@mkdir -p $(RELEASE_DIR)/linux $(RELEASE_DIR)/windows
	@if [ -f $(TAURI_TARGET)/$(WIDGET_BIN) ]; then \
		cp $(TAURI_TARGET)/$(WIDGET_BIN) $(RELEASE_DIR)/linux/$(WIDGET_BIN); \
		echo "→ $(RELEASE_DIR)/linux/$(WIDGET_BIN)"; \
	fi
	@if [ -f $(TAURI_TARGET)/$(WIDGET_BIN).exe ]; then \
		cp $(TAURI_TARGET)/$(WIDGET_BIN).exe $(RELEASE_DIR)/windows/$(WIDGET_BIN).exe; \
		echo "→ $(RELEASE_DIR)/windows/$(WIDGET_BIN).exe"; \
	fi

release: release-go release-widget
	@echo ""
	@echo "Release artefacts under $(RELEASE_DIR)/:"
	@find $(RELEASE_DIR) -type f | sort

release-clean:
	rm -rf $(RELEASE_DIR)

run: linux
	./$(BINARY)

clean:
	rm -f $(BINARY) $(BINARY).exe
	rm -rf $(RELEASE_DIR)

# Format JS / CSS / JSON / HTML in place. Go files are formatted by
# `go fmt` on save; biome handles everything else under backend/web/,
# plugins/, and the toolkit's own configs.
fmt:
	$(BIOME) format --write .

# CI / pre-commit: report formatting drift without writing.
fmt-check:
	$(BIOME) format .

# Lint JS / CSS — surfaces dead code, footguns, missed optional chains.
lint:
	$(BIOME) lint .

# One-shot: lint + format together (read-only, suitable for CI).
check:
	$(BIOME) check .
