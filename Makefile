# spacebar
#
# `make ci` is the gate. It runs what the workflow runs, in the same order,
# with the same tool versions, so a green run here means a green run there.
# Every tool version is pinned in this file and nowhere else: CI installs them
# by asking `make print-<TOOL>_VERSION`, because a floating tool can fail a pull
# request that changed nothing, and it can fail a release, since the release
# gate re-runs this on a tag that cannot be moved.

SHELL := /bin/sh
.DEFAULT_GOAL := help

MODULE := github.com/kmoneil/spacebar
BINARY := spacebar

VERSION := $(shell scripts/version.sh)
COMMIT  := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)

# Pinned tool versions. Bumping one is a commit of its own, so that a change in
# what a linter thinks is never mixed into a change in what the code does.
GOFUMPT_VERSION       := v0.11.0
GOLANGCI_LINT_VERSION := v2.12.2
STATICCHECK_VERSION   := v0.7.0
GOVULNCHECK_VERSION   := v1.7.0
GOCOGNIT_VERSION      := v1.2.1
ADDLICENSE_VERSION    := v1.1.1
GOLICENSES_VERSION    := v1.6.0

# Where the official OAuth client is injected, and the only place it ever is
# (SPEC.md §6.1). Empty here and empty in every build anybody can run: the
# values live in CI secrets, and release.yml passes them through this variable
# on a tag and nowhere else.
#
# A `go build` from source therefore produces a binary with no client, which
# falls through to bring-your-own resolution and says so. That is the intended
# outcome and not a degraded one. A client committed to an Apache-2.0
# repository is a client every fork spends our quota with.
EXTRA_LDFLAGS ?=

# -s -w drops the symbol table and DWARF: this is a CLI, not something anybody
# attaches a debugger to, and it is a third off the binary.
# -trimpath keeps a build reproducible by anybody who is not us, which is the
# only kind of reproducibility worth claiming.
LDFLAGS := -s -w \
	-X $(MODULE)/internal/meta.Version=$(VERSION) \
	-X $(MODULE)/internal/meta.Commit=$(COMMIT) \
	$(EXTRA_LDFLAGS)

GOFLAGS_BUILD := -trimpath -ldflags "$(LDFLAGS)"

# `go env GOOS` rather than $(GOOS), so that this is right both when GOOS is
# exported for a cross-build and when it is not set at all.
BINEXT := $(if $(filter windows,$(shell go env GOOS)),.exe,)

# CGO_ENABLED=0 is a requirement and not an optimisation (SPEC.md §1): it is
# what lets one machine cross-compile every platform this releases for, and it
# is why modernc.org/sqlite is the only SQLite that may ever be considered.
#
# Set on the build and on the licence scan, which has to see the same
# dependency graph the shipped binary has, and deliberately not on the tests:
# `go test -race` needs cgo, and the race detector is worth more here than the
# symmetry. The request budget is shared across concurrent pagination, and a
# counter that undercounts would quietly exceed the limit it exists to enforce.
GO_BUILD_ENV := CGO_ENABLED=0

# SPEC.md §2.1. Anything not on this list fails the build, including
# transitively. BSD-4-Clause is absent on purpose: the advertising clause is
# not compatible with how this is distributed.
ALLOWED_LICENSES := Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause,ISC,0BSD,Unlicense,CC0-1.0

COPYRIGHT_HOLDER := Kevin O'Neil
COPYRIGHT_YEAR   := 2026

# go-licenses decides what is standard library by comparing a package's source
# path against the GOROOT compiled into go-licenses itself. When go.mod names a
# toolchain the machine does not have, the go command fetches it into the
# module cache, GOROOT moves there, and every stdlib package stops being
# recognised as one. The run then dies with "some errors occurred when loading
# direct and transitive dependency packages", which points at nothing. Passing
# the real GOROOT through makes it right under any toolchain.
GOROOT_DIR := $(shell go env GOROOT)
LICENSE_ENV := GOROOT=$(GOROOT_DIR)

# The platforms release.yml publishes. Kept here rather than in the workflow so
# that `make licenses` and the release build cannot disagree about what "every
# platform" means.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: help
help: ## Show this help
	@echo "spacebar $(VERSION)"
	@echo
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}'

## ---------------------------------------------------------------------------
## build
## ---------------------------------------------------------------------------

.PHONY: build
build: ## Build bin/spacebar (honours GOOS and GOARCH for a cross-build)
	$(GO_BUILD_ENV) go build $(GOFLAGS_BUILD) -o bin/$(BINARY)$(BINEXT) ./cmd/$(BINARY)

.PHONY: build-all
build-all: ## Cross-compile every platform this releases for, into dist/
	@for platform in $(PLATFORMS); do \
		goos=$${platform%/*}; goarch=$${platform#*/}; \
		ext=""; [ "$$goos" = windows ] && ext=".exe"; \
		echo "build $$goos/$$goarch"; \
		GOOS=$$goos GOARCH=$$goarch $(GO_BUILD_ENV) \
			go build $(GOFLAGS_BUILD) -o dist/$(BINARY)_$${goos}_$${goarch}/$(BINARY)$$ext ./cmd/$(BINARY) || exit 1; \
	done

.PHONY: install
install: ## Install spacebar into $GOPATH/bin
	$(GO_BUILD_ENV) go install $(GOFLAGS_BUILD) ./cmd/$(BINARY)

.PHONY: clean
clean: ## Remove build output
	rm -rf bin dist

.PHONY: version
version: ## Print the version this build would carry
	@echo $(VERSION)

## ---------------------------------------------------------------------------
## checks
## ---------------------------------------------------------------------------

.PHONY: fmt
fmt: ## Format with gofumpt
	gofumpt -w .

.PHONY: fmt-check
fmt-check: ## Fail if anything is unformatted
	@unformatted=$$(gofumpt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not formatted:"; echo "$$unformatted"; echo "run: make fmt"; exit 1; \
	fi

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: lint
lint: ## golangci-lint
	golangci-lint run

.PHONY: test
test: ## Run the tests with the race detector
	go test -race ./...

.PHONY: cover
cover: ## Run the tests and open a coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "wrote coverage.html"

.PHONY: vuln
vuln: ## govulncheck, fails closed
	govulncheck ./...

# Golden tests live in a file called golden_test.go, which is the convention
# this target depends on: `go test ./... -update` passes the flag to every
# package's test binary, and every package that does not define it fails with
# "flag provided but not defined". Found by the file rather than by the
# testdata/golden directory so that the very first run, before any golden
# exists, still finds the package.
GOLDENPKGS := $(shell find . -name golden_test.go -not -path './bin/*' -not -path './dist/*' -exec dirname {} \; | sort -u)

.PHONY: golden
golden: ## Regenerate the golden files that are the output contract
	@if [ -z "$(strip $(GOLDENPKGS))" ]; then echo "no golden tests"; exit 0; fi
	go test $(GOLDENPKGS) -run 'Golden' -update

.PHONY: contract
contract: golden ## Regenerate the goldens and fail if any of them moved
	@if ! git diff --exit-code --stat -- '*/testdata/golden/*'; then \
		echo; \
		echo "The output contract changed but the golden files were not updated."; \
		echo "Run 'make golden', read every diff, and record the change deliberately."; \
		exit 1; \
	fi

.PHONY: ci
ci: fmt-check vet lint test vuln license-check license-headers-check build ## Everything the gate runs

## ---------------------------------------------------------------------------
## licensing (SPEC.md §2)
## ---------------------------------------------------------------------------

.PHONY: licenses
licenses: ## Regenerate THIRD_PARTY_LICENSES
	@scripts/third-party-licenses.sh

.PHONY: license-check
license-check: ## Fail if any dependency's licence is not on the allowlist
	@for platform in $(PLATFORMS); do \
		echo "license-check $$platform"; \
		GOOS=$${platform%/*} GOARCH=$${platform#*/} $(GO_BUILD_ENV) $(LICENSE_ENV) \
			go-licenses check ./... --allowed_licenses=$(ALLOWED_LICENSES) || exit 1; \
	done

.PHONY: license-headers
license-headers: ## Add the Apache-2.0 header to any file missing one
	addlicense -l apache -c "$(COPYRIGHT_HOLDER)" -y $(COPYRIGHT_YEAR) $$(find . -name '*.go' -not -path './bin/*')

.PHONY: license-headers-check
license-headers-check: ## Fail if any .go file is missing its licence header
	@missing=$$(addlicense -check $$(find . -name '*.go' -not -path './bin/*') 2>&1); \
	if [ -n "$$missing" ]; then \
		echo "missing an Apache-2.0 header:"; echo "$$missing"; \
		echo "run: make license-headers"; exit 1; \
	fi

## ---------------------------------------------------------------------------
## fuzzing
## ---------------------------------------------------------------------------

FUZZTIME ?= 20s

.PHONY: fuzz-packages
fuzz-packages: ## List the packages that have fuzz targets, one per line
	@go list ./... | while read -r pkg; do \
		if go test -list 'Fuzz.*' "$$pkg" 2>/dev/null | grep -q '^Fuzz'; then \
			echo "$${pkg#$(MODULE)/}"; \
		fi; \
	done

FUZZPKGS ?= $(shell $(MAKE) -s fuzz-packages | sed 's|^|./|')

.PHONY: fuzz
fuzz: ## Fuzz every target, FUZZTIME each
	@if [ -z "$(strip $(FUZZPKGS))" ]; then \
		echo "no fuzz targets yet"; exit 0; \
	fi
	@for pkg in $(FUZZPKGS); do \
		for target in $$(go test -list 'Fuzz.*' $$pkg | grep '^Fuzz'); do \
			echo "fuzz $$pkg $$target ($(FUZZTIME))"; \
			go test $$pkg -run '^$$' -fuzz "^$$target$$" -fuzztime=$(FUZZTIME) || exit 1; \
		done; \
	done

## ---------------------------------------------------------------------------
## tooling
## ---------------------------------------------------------------------------

.PHONY: tools
tools: ## Install every pinned tool the gate needs
	go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	go install github.com/uudashr/gocognit/cmd/gocognit@$(GOCOGNIT_VERSION)
	go install github.com/google/addlicense@$(ADDLICENSE_VERSION)
	go install github.com/google/go-licenses@$(GOLICENSES_VERSION)

# A cache key for the whole tool set. The release gate restores ~/go/bin on
# this, so that the one network fetch standing in front of an operation that
# cannot be undone is skipped on the common path.
.PHONY: tools-key
tools-key: ## Print a cache key covering every pinned tool version
	@printf '%s\n' \
		"$(GOFUMPT_VERSION)" "$(GOLANGCI_LINT_VERSION)" "$(STATICCHECK_VERSION)" \
		"$(GOVULNCHECK_VERSION)" "$(GOCOGNIT_VERSION)" "$(ADDLICENSE_VERSION)" \
		"$(GOLICENSES_VERSION)" \
		| sha256sum | cut -c1-16

# CI reads the pins from here rather than declaring them again, because two
# places declaring one version is the drift every comment in this file exists
# to prevent.
print-%: ## Print the value of a variable, e.g. make -s print-GOFUMPT_VERSION
	@echo "$($*)"

.PHONY: hooks
hooks: ## Install the git hooks in .githooks
	git config core.hooksPath .githooks
	@echo "hooks installed; never bypass them with --no-verify"
