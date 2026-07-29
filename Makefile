MODULE  := github.com/nlink-jp/chrome-pilot-mcp
BINARY  := chrome-pilot-mcp
BIN_DIR := dist

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X $(MODULE)/cmd.Version=$(VERSION)"

# macOS Developer ID signing / notarization (see CONVENTIONS.md §Code
# Signing). Defaults match any Developer ID Application cert in the
# keychain and the org-standard notary profile. Builds without these
# fall back to ad-hoc / un-notarized with a one-line warning.
CODESIGN_IDENTITY ?= Developer ID Application
NOTARY_PROFILE    ?= nlink-jp-notary

# darwin ships arm64 only (no amd64, no universal). linux/windows keep their matrix.
PLATFORMS := darwin/arm64 linux/amd64 linux/arm64 windows/amd64

.PHONY: build build-all package test clean help

## build: Build binary for the current OS/Arch → ./dist/chrome-pilot-mcp
build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY) .
	@scripts/codesign-darwin.sh $(BIN_DIR)/$(BINARY) "$(CODESIGN_IDENTITY)"

## build-all: Cross-compile and codesign each darwin build
build-all:
	@mkdir -p $(BIN_DIR)
	@for p in $(PLATFORMS); do os=$${p%/*}; arch=$${p#*/}; \
		ext=""; [ "$$os" = windows ] && ext=".exe"; \
		echo "Building $(BIN_DIR)/$(BINARY)-$$os-$$arch$$ext..."; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY)-$$os-$$arch$$ext . ; \
	done
	@scripts/codesign-darwin.sh $(BIN_DIR)/$(BINARY)-darwin-arm64 "$(CODESIGN_IDENTITY)" "$(BINARY)"

## package: archive each platform as <name>-v<version>-<os>-<arch>.<ext>
## (darwin/windows=zip, linux=tar.gz); canonical binary + README + LICENSE
## inside; notarize the darwin arm64 zip.
package: build-all
	@cd $(BIN_DIR) && for p in $(PLATFORMS); do os=$${p%/*}; arch=$${p#*/}; \
		ext=""; [ "$$os" = windows ] && ext=".exe"; \
		stage=_pkg; rm -rf $$stage; mkdir -p $$stage; \
		cp "$(BINARY)-$$os-$$arch$$ext" "$$stage/$(BINARY)$$ext"; \
		cp ../README.md ../LICENSE $$stage/; \
		base="$(BINARY)-$(VERSION)-$$os-$$arch"; \
		if [ "$$os" = linux ]; then ( cd $$stage && tar -czf "../$$base.tar.gz" * ); \
		else ( cd $$stage && zip -q "../$$base.zip" * ); fi; \
		rm -rf $$stage; \
	done
	@scripts/notarize-darwin.sh $(BIN_DIR)/$(BINARY)-$(VERSION)-darwin-arm64.zip "$(NOTARY_PROFILE)"

## test: Run all unit tests
test:
	go test ./...

## clean: Remove build artifacts
clean:
	rm -rf $(BIN_DIR)

## help: Show available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

# Homebrew tap generation (see scripts/release-brew.mk). After `make package`,
# `make brew` generates this formula from the built darwin-arm64 zip into the
# local nlink-jp/homebrew-tap checkout. The package target is unchanged.
BREW_KIND := formula
BREW_DESC := Zero-dependency Chrome automation MCP server (CDP direct)
include scripts/release-brew.mk
