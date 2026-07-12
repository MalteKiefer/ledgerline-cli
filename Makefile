# ledgerline-cli build tooling.
#
# `make build`  — build a stamped binary for the host into ./bin
# `make release` — cross-compile Linux and macOS binaries into ./dist
# `make test`   — run the test suite
# `make check`  — vet + gofmt verification + tests

BINARY      := ledgerline-cli
PKG         := github.com/MalteKiefer/ledgerline-cli/internal/version

# Version derives from the current git tag; commit and date are always stamped.
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(PKG).Version=$(VERSION) \
	-X $(PKG).Commit=$(COMMIT) \
	-X $(PKG).BuildDate=$(BUILD_DATE)

# The verified, supported release targets.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build
build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .
	@echo "built bin/$(BINARY) $(VERSION)"

.PHONY: install
install:
	CGO_ENABLED=0 go install -trimpath -ldflags "$(LDFLAGS)" .

.PHONY: release
release:
	@mkdir -p dist
	@set -e; for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		out=dist/$(BINARY)-$(VERSION)-$$os-$$arch; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "$(LDFLAGS)" -o $$out . ; \
	done
	@echo "release binaries in ./dist"

.PHONY: test
test:
	go test ./...

.PHONY: check
check: test
	go vet ./...
	@test -z "$$(gofmt -l . )" || (echo "gofmt needed on:" && gofmt -l . && exit 1)

.PHONY: clean
clean:
	rm -rf bin dist
