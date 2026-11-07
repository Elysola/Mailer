# WASM plugins are build artifacts, not sources. Build them before running
# `mailer` (or `mailer plugins test`); CI does this automatically.
#
# TinyGo is required and version-sensitive: the plugin ABI is exercised
# against a specific toolchain, so pin it rather than tracking latest.

TINYGO ?= tinygo
TINYGO_TARGET ?= wasip1

# Build metadata injected into the binary. Release builds pass VERSION/COMMIT/
# DATE on the command line; a local `make build` falls back to "dev" plus the
# commit Go records in the build info, so the banner is never misleading.
VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

.PHONY: all build test plugins clean-plugins release-check

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o mailer .

test:
	go vet ./...
	go test -race ./...

# Build every plugin into <dir>/<name>.wasm, matching the default entrypoint
# convention in internal/plugin (a manifest at x/plugin.yaml defaults to
# x.wasm).
#
# The iteration lives in scripts/plugins.sh rather than in a recipe: a compound
# shell statement written inline is expanded by make first, and lint tools then
# read the mangled result as shell (producing spurious SC2046/SC1089). Keeping
# the recipe a single simple command avoids that class of false positive.
plugins:
	@./scripts/plugins.sh build

clean-plugins:
	@./scripts/plugins.sh clean

# Validate the GoReleaser config and do a throwaway snapshot build. Catches a
# broken .goreleaser.yaml before a tag turns it into a failed release.
release-check:
	goreleaser check
	COSIGN_SKIP=1 goreleaser build --snapshot --clean
