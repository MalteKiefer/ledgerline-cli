# ledgerline-cli build tooling.
#
# `make build`  — build a stamped binary for the host into ./bin
# `make release` — cross-compile Linux and macOS binaries into ./dist
# `make test`   — run the test suite
# `make check`  — vet + gofmt verification + tests

BINARY      := ledgerline-cli
PKG         := github.com/MalteKiefer/ledgerline-cli/internal/version

# Version derives from the current git tag; commit is always stamped. BUILD_DATE
# is the COMMIT date (UTC), not wall-clock, so a given commit builds byte-for-byte
# reproducibly (verified by `make repro-verify`); it falls back to now outside a
# git tree.
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE  ?= $(shell TZ=UTC git show -s --date=format-local:'%Y-%m-%dT%H:%M:%SZ' --format=%cd HEAD 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)

# Pinned SBOM generator (CycloneDX). Bump deliberately; the committed sbom.json is
# diffed in CI so an unexplained dependency change blocks the merge.
CYCLONEDX_VERSION := v1.10.0

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

.PHONY: lint
lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./...

.PHONY: sbom
sbom:
	go run github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@$(CYCLONEDX_VERSION) \
		mod -json -noserial -licenses -output sbom.json .
	@echo "wrote sbom.json"

# sbom-verify regenerates the SBOM and fails if it differs from the committed
# sbom.json (ignoring the metadata timestamp), so an unexplained dependency change
# blocks CI (§20 supply chain).
.PHONY: sbom-verify
sbom-verify:
	@go run github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@$(CYCLONEDX_VERSION) \
		mod -json -noserial -licenses -output sbom.new.json .
	@# Ignore the metadata timestamp and the main module's own git pseudo-version
	@# (both move on every commit); the point is to catch DEPENDENCY drift.
	@grep -Ev '("timestamp"|ledgerline-cli@)' sbom.json     > sbom.a.tmp
	@grep -Ev '("timestamp"|ledgerline-cli@)' sbom.new.json > sbom.b.tmp
	@if ! diff -u sbom.a.tmp sbom.b.tmp; then \
		rm -f sbom.new.json sbom.a.tmp sbom.b.tmp; \
		echo "SBOM drift: regenerate with 'make sbom' and commit the change"; exit 1; \
	fi
	@rm -f sbom.new.json sbom.a.tmp sbom.b.tmp
	@echo "sbom.json is up to date"

# repro-verify builds the host binary twice and fails if the bytes differ,
# proving the build is reproducible from source (§20/§27).
.PHONY: repro-verify
repro-verify:
	@mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY).repro1 .
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY).repro2 .
	@a=$$(shasum -a 256 dist/$(BINARY).repro1 | cut -d' ' -f1); \
	 b=$$(shasum -a 256 dist/$(BINARY).repro2 | cut -d' ' -f1); \
	 rm -f dist/$(BINARY).repro1 dist/$(BINARY).repro2; \
	 if [ "$$a" != "$$b" ]; then echo "NON-REPRODUCIBLE: $$a != $$b"; exit 1; fi; \
	 echo "reproducible build ok ($$a)"

.PHONY: clean
clean:
	rm -rf bin dist
