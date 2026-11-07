# WASM plugins are build artifacts, not sources. Build them before running
# `mailer` (or `mailer plugins test`); CI does this automatically.
#
# TinyGo is required and version-sensitive: the plugin ABI is exercised
# against a specific toolchain, so pin it rather than tracking latest.

TINYGO ?= tinygo
TINYGO_TARGET ?= wasip1

# Every directory holding a plugin go.mod. Empty is fine: the recipes below
# then simply do nothing.
PLUGIN_DIRS := $(patsubst %/go.mod,%,$(wildcard plugins/*/go.mod))

.PHONY: all build test plugins clean-plugins

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o mailer .

test:
	go vet ./...
	go test -race ./...

# Build every plugin into <dir>/<name>.wasm, matching the default entrypoint
# convention in internal/plugin (a manifest at x/plugin.yaml defaults to
# x.wasm).
#
# The per-plugin work lives in scripts/build-plugin.sh so the loop stays out
# of make's recipe expansion.
plugins:
	@for dir in $(PLUGIN_DIRS); do \
		./scripts/build-plugin.sh "$$dir" || exit 1; \
	done

clean-plugins:
	@for dir in $(PLUGIN_DIRS); do \
		rm -f "$$dir/$$(basename "$$dir").wasm"; \
	done
