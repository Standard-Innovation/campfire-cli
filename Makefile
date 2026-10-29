.PHONY: build test test-unit fmt fmt-check vet lint tidy tidy-check race-test vuln secrets \
	replace-check security check release-check clean tools help

BINARY := $(CURDIR)/bin/campfire
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# Default target — local CI gate
.DEFAULT_GOAL := check

help:
	@echo "Campfire CLI"
	@echo ""
	@echo "  make build          Build the CLI to bin/campfire"
	@echo "  make test-unit      Run unit tests (no Campfire required)"
	@echo "  make fmt            Format Go source files"
	@echo "  make fmt-check      Check formatting (CI gate)"
	@echo "  make vet            Run go vet"
	@echo "  make lint           Run golangci-lint"
	@echo "  make tidy-check     Verify go.mod/go.sum tidiness"
	@echo "  make race-test      Run unit tests with race detector"
	@echo "  make vuln           Run govulncheck"
	@echo "  make secrets        Run gitleaks secret scan"
	@echo "  make security       lint + vuln + secrets"
	@echo "  make check          fmt-check + vet + lint + tidy-check + race-test"
	@echo "  make release-check  check + replace-check + vuln"
	@echo "  make tools          Install dev tools"

build:
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/campfire

test-unit:
	go test ./...

test: test-unit

fmt:
	gofmt -s -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "Files not formatted:"; gofmt -l .; exit 1)

vet:
	go vet ./...

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

# Verify go.mod/go.sum tidiness (non-mutating)
tidy-check:
	@cp go.mod go.mod.bak && cp go.sum go.sum.bak
	@go mod tidy
	@if ! diff -q go.mod go.mod.bak >/dev/null 2>&1 || ! diff -q go.sum go.sum.bak >/dev/null 2>&1; then \
		mv go.mod.bak go.mod; mv go.sum.bak go.sum; \
		echo "go.mod or go.sum is not tidy — run 'go mod tidy'"; \
		exit 1; \
	fi
	@mv go.mod.bak go.mod && mv go.sum.bak go.sum

race-test:
	go test -race -count=1 ./internal/...

vuln:
	govulncheck ./...

secrets:
	@if ! command -v gitleaks >/dev/null 2>&1; then \
		echo "Skipping gitleaks (binary not found)"; \
	else \
		gitleaks detect --source . --verbose; \
	fi

# Guard against replace directives in go.mod
replace-check:
	@if grep -q '^replace' go.mod; then \
		echo "ERROR: go.mod contains replace directives"; \
		grep '^replace' go.mod; \
		exit 1; \
	fi

security: lint vuln secrets

check: fmt-check vet lint tidy-check race-test

release-check: check replace-check vuln

tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest
	@echo "For gitleaks, install via: brew install gitleaks"

clean:
	rm -rf bin/ dist/ completions/
	go clean
