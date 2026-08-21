# ledgerline-cli build tooling.
#
# `make build`   — build a stamped binary for the host into ./bin
# `make release` — cross-compile Linux, macOS and Windows binaries into ./dist
# `make package` — build .deb and .rpm packages (amd64 + arm64) into ./dist
# `make installer-windows` — build the Windows setup .exe into ./dist
# `make test`    — run the test suite
# `make check`   — vet + gofmt verification + tests (+ race)

BINARY      := ledgerline-cli
GUI_BINARY  := ledgerline-gui
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

# Pinned Linux packager (nfpm) for the .deb/.rpm artefacts.
NFPM_VERSION := v2.47.0

# Pinned Windows resource compiler. It stamps the icon, the version fields and
# the side-by-side manifest (themed controls + per-monitor DPI) into the two
# .exe files; Go itself can only link a .syso, not produce one.
GOVERSIONINFO_VERSION := v1.5.0

# The icon is drawn by ./cmd/gen-icon from the same code the tray uses, so the
# executable, the installer, the shortcut and the notification area cannot show
# different marks.
ICON := packaging/windows/ledgerline.ico

# Numeric version parts for the Windows VERSIONINFO resource, which only accepts
# integers: 0.7.4-66-gabc1234 becomes 0.7.4.66.
# Every part defaults to 0: a checkout with no tags in reach describes as a bare
# commit hash, which parses to nothing, and goversioninfo then reads the next
# flag as the value of the missing one, and reports the next flag name as an
# invalid value. A version resource of 0.0.0.0 is wrong but harmless; a build
# that dies on a shallow clone is not.
VER_MAJOR_RAW := $(shell echo $(VERSION) | sed 's/[^0-9.].*//' | cut -d. -f1)
VER_MINOR_RAW := $(shell echo $(VERSION) | sed 's/[^0-9.].*//' | cut -d. -f2)
VER_PATCH_RAW := $(shell echo $(VERSION) | sed 's/[^0-9.].*//' | cut -d. -f3)
VER_MAJOR := $(if $(VER_MAJOR_RAW),$(VER_MAJOR_RAW),0)
VER_MINOR := $(if $(VER_MINOR_RAW),$(VER_MINOR_RAW),0)
VER_PATCH := $(if $(VER_PATCH_RAW),$(VER_PATCH_RAW),0)
VER_BUILD_RAW := $(shell echo $(VERSION) | sed -n 's/^[0-9.]*-\([0-9]*\)-.*/\1/p')
VER_BUILD := $(if $(VER_BUILD_RAW),$(VER_BUILD_RAW),0)

# Package versions must be digits-and-dots for both dpkg and rpm; a dirty or
# post-tag `git describe` string ("0.7.4-3-gabc1234-dirty") is normalised here.
PKG_VERSION := $(subst -,.,$(VERSION))

LDFLAGS := -s -w \
	-X $(PKG).Version=$(VERSION) \
	-X $(PKG).Commit=$(COMMIT) \
	-X $(PKG).BuildDate=$(BUILD_DATE)

# The verified, supported release targets. Windows ships as a plain .exe (no
# CGO, no installer): the client is a single static binary everywhere.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

# Arches that get a .deb and .rpm.
PKG_ARCHES := amd64 arm64

# The tray GUI ships for Windows first; the Linux and macOS trays are separate
# work (StatusNotifierItem, Cocoa) and are not built here yet.
GUI_PLATFORMS := windows/amd64 windows/arm64

.PHONY: icon
icon:
	@go run ./cmd/gen-icon -o $(ICON)

# resources writes one .syso per Windows architecture into each main package.
# Go links a .syso automatically when its name matches the target, and ignores
# it entirely on other platforms, so the Linux and macOS builds are unaffected.
.PHONY: resources
resources: icon
	@set -e; for arch in amd64 arm64; do \
		flags="-64"; [ "$$arch" = "arm64" ] && flags="-arm -64"; \
		go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@$(GOVERSIONINFO_VERSION) \
			$$flags -o resource_windows_$$arch.syso \
			-ver-major $(VER_MAJOR) -ver-minor $(VER_MINOR) -ver-patch $(VER_PATCH) -ver-build $(VER_BUILD) \
			-product-ver-major $(VER_MAJOR) -product-ver-minor $(VER_MINOR) \
			-product-ver-patch $(VER_PATCH) -product-ver-build $(VER_BUILD) \
			-file-version "$(VERSION)" -product-version "$(VERSION)" \
			packaging/windows/versioninfo.json ; \
		cp resource_windows_$$arch.syso cmd/$(GUI_BINARY)/ ; \
	done
	@echo "wrote Windows resource objects"

.PHONY: build
build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .
	@echo "built bin/$(BINARY) $(VERSION)"

.PHONY: install
install:
	CGO_ENABLED=0 go install -trimpath -ldflags "$(LDFLAGS)" .

# install-windows-local replaces the installed programs with the ones in ./bin,
# for testing a change on the machine that built it without stepping through the
# installer. The work is in a PowerShell script rather than inline here because
# the elevation and the quoting are unreadable as a make recipe.
.PHONY: install-windows-local
install-windows-local:
	powershell -NoProfile -ExecutionPolicy Bypass -File packaging/windows/install-local.ps1

.PHONY: release
release: resources
	@mkdir -p dist
	@set -e; for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		out=dist/$(BINARY)-$(VERSION)-$$os-$$arch$$ext; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "$(LDFLAGS)" -o $$out . ; \
	done
	@$(MAKE) --no-print-directory release-gui
	@echo "release binaries in ./dist"

# The tray GUI links with -H=windowsgui so starting it from the Start menu does
# not flash a console window. Everything else matches the CLI build.
.PHONY: release-gui
release-gui:
	@mkdir -p dist
	@set -e; for platform in $(GUI_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		out=dist/$(GUI_BINARY)-$(VERSION)-$$os-$$arch$$ext; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "$(LDFLAGS) -H=windowsgui" -o $$out ./cmd/$(GUI_BINARY) ; \
	done

.PHONY: test
test:
	go test ./...

# The client is concurrent (parallel uploads, the sync engine, the watch
# service), so the race detector is part of the gate, not an optional extra.
.PHONY: test-race
test-race:
	go test -race ./...

.PHONY: check
check: test test-race
	go vet ./...
	@test -z "$$(gofmt -l . )" || (echo "gofmt needed on:" && gofmt -l . && exit 1)

# tidy-verify fails when go.mod/go.sum are not what `go mod tidy` would write,
# so an undeclared or stale dependency cannot slip in; `go mod verify` then
# checks every module in the cache against its go.sum checksum.
.PHONY: tidy-verify
tidy-verify:
	@cp go.mod go.mod.bak; cp go.sum go.sum.bak
	@go mod tidy
	@if ! diff -q go.mod go.mod.bak >/dev/null || ! diff -q go.sum go.sum.bak >/dev/null; then \
		diff -u go.mod.bak go.mod || true; diff -u go.sum.bak go.sum || true; \
		mv go.mod.bak go.mod; mv go.sum.bak go.sum; \
		echo "go.mod/go.sum not tidy: run 'go mod tidy' and commit"; exit 1; \
	fi
	@rm -f go.mod.bak go.sum.bak
	@go mod verify

.PHONY: lint
lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./...

# completions are generated once from a host build; their content does not
# depend on the target architecture, so the same files ship in every package.
.PHONY: completions
completions: build
	@mkdir -p dist/completions
	./bin/$(BINARY) completion bash > dist/completions/$(BINARY).bash
	./bin/$(BINARY) completion zsh  > dist/completions/$(BINARY).zsh
	./bin/$(BINARY) completion fish > dist/completions/$(BINARY).fish
	@echo "wrote dist/completions"

# package builds a .deb and .rpm per arch from the release binaries, so the
# published packages contain the exact bytes that were checksummed.
.PHONY: package
package: release completions
	@mkdir -p dist/pkg
	@set -e; for arch in $(PKG_ARCHES); do \
		cp dist/$(BINARY)-$(VERSION)-linux-$$arch dist/pkg/$(BINARY); \
		for format in deb rpm; do \
			echo "packaging $$format/$$arch"; \
			PKG_VERSION=$(PKG_VERSION) PKG_ARCH=$$arch \
			go run github.com/goreleaser/nfpm/v2/cmd/nfpm@$(NFPM_VERSION) package \
				--config packaging/nfpm.yaml --packager $$format --target dist/ ; \
		done; \
	done
	@rm -rf dist/pkg
	@echo "packages in ./dist"

# installer-windows wraps the Windows CLI + tray GUI into one setup .exe per
# arch (Start-menu shortcuts, PATH entry, optional autostart, uninstaller).
# makensis resolves File paths relative to the script's own directory and wants
# backslash separators, hence the ..\..\dist form.
.PHONY: installer-windows
installer-windows: release icon
	@command -v makensis >/dev/null || { echo "makensis not found (apt-get install nsis)"; exit 1; }
	@set -e; for arch in $(PKG_ARCHES); do \
		echo "building dist/$(BINARY)-setup-$(VERSION)-$$arch.exe"; \
		makensis -V2 \
			-DVERSION=$(PKG_VERSION) \
			-DICON=..\\..\\$(ICON) \
			-DCLI_EXE=..\\..\\dist\\$(BINARY)-$(VERSION)-windows-$$arch.exe \
			-DGUI_EXE=..\\..\\dist\\$(GUI_BINARY)-$(VERSION)-windows-$$arch.exe \
			-DOUTFILE=..\\..\\dist\\$(BINARY)-setup-$(VERSION)-$$arch.exe \
			packaging/windows/installer.nsi ; \
	done
	@echo "installers in ./dist"

# SBOM_ENV pins the module graph the generator resolves to the CI platform, so a
# regeneration from a Windows or macOS workstation produces the same committed
# file instead of one whose purls all carry that host's goos/goarch — which
# sbom-verify would then reject as drift.
#
# It applies to the ANALYSIS, not to building the generator: `go run` obeys
# GOOS too, so setting it up front produced a Linux binary the workstation then
# could not execute ("executable file not found"). The tool is therefore built
# for the host first and run with the pinned environment afterwards.
SBOM_ENV := GOOS=linux GOARCH=amd64
SBOM_TOOL := $(shell go env GOPATH)/bin/cyclonedx-gomod$(shell go env GOEXE)

.PHONY: sbom-tool
sbom-tool:
	@go install github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@$(CYCLONEDX_VERSION)

.PHONY: sbom
sbom: sbom-tool
	$(SBOM_ENV) "$(SBOM_TOOL)" mod -json -noserial -licenses -output sbom.json .
	@echo "wrote sbom.json"

# sbom-verify regenerates the SBOM and fails if it differs from the committed
# sbom.json (ignoring non-deterministic noise), so an unexplained dependency
# change blocks CI (§20 supply chain).
.PHONY: sbom-verify
sbom-verify: sbom-tool
	@$(SBOM_ENV) "$(SBOM_TOOL)" mod -json -noserial -licenses -output sbom.new.json .
	@# Ignore: the metadata timestamp (moves every run); metadata.tools[].hashes
	@# (the CycloneDX-gomod BINARY's own MD5/SHA*, which differ per machine and
	@# toolchain patch and are not part of our dependency graph); the main module's own git pseudo-version
	@# (moves every commit; the pinned deps are all tagged releases, so any
	@# 0.0.0-<date>-<hash> pseudo-version line is the main module). The point is to
	@# catch DEPENDENCY drift, not these.
	@jq 'del(.metadata.timestamp, .metadata.tools)' sbom.json     | grep -Ev '(ledgerline-cli@|[0-9]{14}-[0-9a-f]{12})' > sbom.a.tmp
	@jq 'del(.metadata.timestamp, .metadata.tools)' sbom.new.json | grep -Ev '(ledgerline-cli@|[0-9]{14}-[0-9a-f]{12})' > sbom.b.tmp
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
