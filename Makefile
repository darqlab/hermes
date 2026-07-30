.PHONY: build test install clean cross release

BINARY=hermes
DIST_DIR=dist
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

build:
	go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) .

test:
	go test ./... -race

install: build
	cp $(BINARY) $(HOME)/.local/bin/$(BINARY)

cross:
	mkdir -p $(DIST_DIR)
	GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(DIST_DIR)/$(BINARY)-linux-amd64 .
	GOOS=linux   GOARCH=arm64 go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(DIST_DIR)/$(BINARY)-linux-arm64 .

release: clean
	mkdir -p $(DIST_DIR)
	GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(DIST_DIR)/$(BINARY)-linux-amd64 .
	GOOS=linux   GOARCH=arm64 go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(DIST_DIR)/$(BINARY)-linux-arm64 .
	GOOS=darwin  GOARCH=amd64 go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(DIST_DIR)/$(BINARY)-darwin-amd64 .
	GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(DIST_DIR)/$(BINARY)-darwin-arm64 .
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(DIST_DIR)/$(BINARY)-windows-amd64.exe .
	@echo ""
	@echo "=== release binaries ($(VERSION)) ==="
	@ls -lh $(DIST_DIR)/
	@echo ""
	@echo "=== sha256 ==="
	@cd $(DIST_DIR) && sha256sum * > checksums.txt 2>/dev/null || true
	@cat $(DIST_DIR)/checksums.txt 2>/dev/null || echo "  (sha256sum not available)"

clean:
	rm -f $(BINARY)
	rm -rf $(DIST_DIR)
