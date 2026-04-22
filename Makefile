.PHONY: build build-nofitz test test-nofitz lint audit clean install sample \
        sample-describe sample-ocr compare-msft compare-msft-llm compare-msft-install \
        build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 build-all \
        deps-check install-lint-tools vet fmt tidy

SHELL := /bin/bash

BINARY := go-markitdown
PKG    := ./cmd/go-markitdown
DIST   := dist

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
GOFLAGS :=
BUILD_FLAGS := -trimpath -ldflags "$(LDFLAGS)"

GOLANGCI_LINT := $(shell command -v golangci-lint 2>/dev/null)
GOVULNCHECK   := $(shell command -v govulncheck 2>/dev/null)
GITLEAKS      := $(shell command -v gitleaks 2>/dev/null)

## --- Default build ---

build: ## Build the CLI with the default feature set (includes go-fitz).
	go build $(BUILD_FLAGS) -o $(BINARY) $(PKG)

build-nofitz: ## Build the CLI without go-fitz (pure Go, degraded format support).
	go build $(BUILD_FLAGS) -tags nofitz -o $(BINARY)-nofitz $(PKG)

## --- Tests / checks ---

test: ## Run tests (default build tag).
	go test ./... -count=1

test-nofitz: ## Run tests with the nofitz build tag.
	go test -tags nofitz ./... -count=1

vet: ## Run go vet on all packages.
	go vet ./...

fmt: ## gofmt over the tree.
	gofmt -s -w .

tidy: ## Tidy up go.mod / go.sum.
	go mod tidy

install-lint-tools: ## Install golangci-lint, govulncheck, and gitleaks if missing.
	@if [ -z "$(GOLANGCI_LINT)" ]; then \
		echo "installing golangci-lint..."; \
		GOBIN=$$(go env GOPATH)/bin go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; \
	fi
	@if [ -z "$(GOVULNCHECK)" ]; then \
		echo "installing govulncheck..."; \
		GOBIN=$$(go env GOPATH)/bin go install golang.org/x/vuln/cmd/govulncheck@latest; \
	fi
	@if [ -z "$(GITLEAKS)" ]; then \
		echo "installing gitleaks..."; \
		GOBIN=$$(go env GOPATH)/bin go install github.com/zricethezav/gitleaks/v8@latest; \
	fi

lint: install-lint-tools ## Run golangci-lint.
	$$(command -v golangci-lint || echo $$(go env GOPATH)/bin/golangci-lint) run ./...

audit: lint ## Run lint + govulncheck + gitleaks. Auto-installs any missing tools.
	@if [ -z "$(GOVULNCHECK)" ] && ! command -v govulncheck >/dev/null 2>&1; then \
		echo "installing govulncheck..."; \
		GOBIN=$$(go env GOPATH)/bin go install golang.org/x/vuln/cmd/govulncheck@latest; \
	fi
	$$(command -v govulncheck || echo $$(go env GOPATH)/bin/govulncheck) ./...
	@if [ -z "$(GITLEAKS)" ] && ! command -v gitleaks >/dev/null 2>&1; then \
		echo "installing gitleaks..."; \
		GOBIN=$$(go env GOPATH)/bin go install github.com/zricethezav/gitleaks/v8@latest; \
	fi
	$$(command -v gitleaks || echo $$(go env GOPATH)/bin/gitleaks) detect --source . --no-git --config .gitleaks.toml -v

clean: ## Remove built binaries and dist/.
	rm -f $(BINARY) $(BINARY)-nofitz
	rm -rf $(DIST) samples

install: build ## Copy the binary into $HOME/bin.
	install -d $(HOME)/bin
	install -m 755 $(BINARY) $(HOME)/bin/$(BINARY)

## --- Local dev sample runner ---

sample: build ## Run the CLI against every testdata fixture and dump output to samples/.
	@mkdir -p samples
	@for f in docconv/testdata/test.*; do \
		name=$$(basename $$f); \
		echo "==> $$name"; \
		./$(BINARY) convert $$f > samples/$$name.md 2>samples/$$name.err || true; \
	done
	@echo "Wrote samples/ for manual inspection."

# SAMPLE_DESCRIBE_FIXTURE defaults to the DOCX fixture (has embedded images that
# our OOXML walker surfaces). Override on the command line if you want to try a
# different fixture, e.g. `make sample-describe SAMPLE_DESCRIBE_FIXTURE=my.docx`.
SAMPLE_DESCRIBE_FIXTURE ?= docconv/testdata/test.docx

sample-describe: build ## Run convert --include-images --describer against a fixture. Uses examples/describer-anthropic.sh, which auto-loads .env.
	@mkdir -p samples
	@echo "==> convert with --describer on $(SAMPLE_DESCRIBE_FIXTURE)"
	./$(BINARY) convert --verbose --include-images \
	    --describer ./examples/describer-anthropic.sh \
	    $(SAMPLE_DESCRIBE_FIXTURE) > samples/describe.md
	@echo "Wrote samples/describe.md"

SAMPLE_OCR_FIXTURE ?= docconv/testdata/test.pdf

sample-ocr: build ## Run convert --ocr-fallback --describer against a fixture (needs a textless/scanned doc to actually fire).
	@mkdir -p samples
	@echo "==> convert with --ocr-fallback on $(SAMPLE_OCR_FIXTURE)"
	@echo "    (note: OCR only fires when text extraction returns empty;"
	@echo "     try a scanned/image-only PDF to actually invoke the describer)"
	./$(BINARY) convert --verbose --ocr-fallback \
	    --describer ./examples/describer-anthropic.sh \
	    $(SAMPLE_OCR_FIXTURE) > samples/ocr.md
	@echo "Wrote samples/ocr.md"

## --- Parity comparison vs Microsoft markitdown ---

compare-msft: build ## Compare our output against Microsoft markitdown. Needs `markitdown` on PATH (see compare-msft-install).
	./scripts/compare-msft.sh

compare-msft-llm: build ## Parity compare with OpenAI image descriptions on both sides. Needs OPENAI_API_KEY (uses .env) and `make compare-msft-install` to have run.
	@if [ ! -x "$$HOME/.local/share/pipx/venvs/markitdown/bin/python" ]; then \
		echo "ERROR: markitdown pipx venv not found. Run 'make compare-msft-install' first." >&2; \
		exit 1; \
	fi
	@if ! "$$HOME/.local/share/pipx/venvs/markitdown/bin/python" -c "import openai" 2>/dev/null; then \
		echo "Injecting 'openai' into the markitdown pipx venv..."; \
		pipx inject markitdown openai; \
	fi
	./scripts/compare-msft.sh --llm

compare-msft-install: ## Install Microsoft markitdown via pipx (with `openai` injected for LLM parity).
	@if ! command -v pipx >/dev/null 2>&1; then \
		echo "ERROR: pipx is not on PATH. Install pipx first: https://pipx.pypa.io/stable/installation/" >&2; \
		exit 1; \
	fi
	pipx install 'markitdown[all]'
	pipx inject markitdown openai

## --- Cross-compile via zig cc ---

deps-check: ## Verify cross-compile prerequisites (zig on PATH).
	@if ! command -v zig >/dev/null 2>&1; then \
		echo "ERROR: zig is not on PATH. Install from https://ziglang.org/download/ to cross-compile." >&2; \
		exit 1; \
	fi

define xbuild
	@mkdir -p $(DIST)
	CGO_ENABLED=1 GOOS=$(1) GOARCH=$(2) \
	    CC="zig cc -target $(3)" \
	    CXX="zig c++ -target $(3)" \
	    go build $(BUILD_FLAGS) -o $(DIST)/$(BINARY)-$(1)-$(2) $(PKG)
	@echo "built $(DIST)/$(BINARY)-$(1)-$(2)"
endef

build-linux-amd64: deps-check ## Cross-compile linux/amd64.
	$(call xbuild,linux,amd64,x86_64-linux-gnu)

build-linux-arm64: deps-check ## Cross-compile linux/arm64.
	$(call xbuild,linux,arm64,aarch64-linux-gnu)

build-darwin-amd64: deps-check ## Cross-compile darwin/amd64.
	$(call xbuild,darwin,amd64,x86_64-macos-none)

build-darwin-arm64: deps-check ## Cross-compile darwin/arm64.
	$(call xbuild,darwin,arm64,aarch64-macos-none)

build-all: build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 ## Cross-compile all supported targets.

## --- Help ---

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
