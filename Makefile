BINARY      := rl-toolkit
GO_FLAGS    := -trimpath
LD_FLAGS    := -s -w

.PHONY: all linux windows clean run

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
