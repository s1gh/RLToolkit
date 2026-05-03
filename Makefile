BINARY      := rl-toolkit
GO_FLAGS    := -trimpath
LD_FLAGS    := -s -w

# Biome lives in node_modules/.bin after `npm install`. Keep the path
# explicit so `make fmt` / `make lint` works without requiring a global
# install or an active shell PATH.
BIOME       := ./node_modules/.bin/biome

.PHONY: all linux windows clean run fmt fmt-check lint check

all: linux windows

linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" -o $(BINARY) .

windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build $(GO_FLAGS) -ldflags="$(LD_FLAGS)" -o $(BINARY).exe .

run: linux
	./$(BINARY)

clean:
	rm -f $(BINARY) $(BINARY).exe

# Format JS / CSS / JSON / HTML in place. Go files are formatted by
# `go fmt` on save; biome handles everything else under web/, plugins/,
# and the toolkit's own configs.
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
