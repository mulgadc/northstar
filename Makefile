GO_PROJECT_NAME := northstar
DOCKER_IMAGE := calacode/northstar-dns
SHELL := /bin/bash

# Quiet-mode filters (active when QUIET=1, set by preflight via recursive make)
# Note: grep pipelines use PIPESTATUS[0] so the exit status of `go test`
# propagates through the filter — otherwise a test failure is swallowed by
# grep's own (success) exit code and preflight prints "passed" on red.
ifdef QUIET
  _Q     = @
  _COVQ  = 2>&1 | { grep -Ev '^\s*(ok|PASS|\?|=== RUN|--- PASS:)\s' | grep -v 'coverage: 0\.0%' || true; }; exit $${PIPESTATUS[0]}
  _RACEQ = 2>&1 | { grep -Ev '^\s*(ok|PASS|\?|=== RUN|--- PASS:)\s' || true; }; exit $${PIPESTATUS[0]}
else
  _Q     =
  _COVQ  =
  _RACEQ =
endif

build:
	$(MAKE) go_build

# GO commands
VERSION ?= $(shell git describe --tags --always --dirty)
LDFLAGS := -s -w -X main.Version=$(VERSION)

go_build:
	@echo -e "\n....Building $(GO_PROJECT_NAME)"
	GOFIPS140=v1.0.0 go build -ldflags "$(LDFLAGS)" -o ./bin/$(GO_PROJECT_NAME) ./cmd/northstar

# Build and run locally (override the listen port for non-root, e.g. PORT=5353)
run: go_build
	./bin/$(GO_PROJECT_NAME)

# Preflight — runs the same checks as GitHub Actions (lint + vuln + tests).
# Use this before committing to catch CI failures locally.
preflight:
	@$(MAKE) --no-print-directory QUIET=1 lint govulncheck test-cover diff-coverage test-race
	@echo -e "\n ✅ Preflight passed — safe to commit."

# Run unit tests
test:
	@echo -e "\n....Running tests for $(GO_PROJECT_NAME)...."
	NORTHSTAR_LOG_IGNORE=1 go test -timeout 120s ./...

# Run unit tests with coverage profile
COVERPROFILE ?= coverage.out
test-cover:
	@echo -e "\n....Running tests with coverage for $(GO_PROJECT_NAME)...."
	$(_Q)NORTHSTAR_LOG_IGNORE=1 go test -timeout 120s -coverprofile=$(COVERPROFILE) -covermode=atomic ./... $(_COVQ)
	@scripts/check-coverage.sh $(COVERPROFILE) $(QUIET)

# Run unit tests with race detector
test-race:
	@echo -e "\n....Running tests with race detector for $(GO_PROJECT_NAME)...."
	$(_Q)NORTHSTAR_LOG_IGNORE=1 go test -race -timeout 300s ./... $(_RACEQ)

# Check that new/changed code meets coverage threshold (runs tests first)
diff-coverage: test-cover
	@QUIET=$(QUIET) scripts/diff-coverage.sh $(COVERPROFILE)

bench:
	@echo -e "\n....Running benchmarks for $(GO_PROJECT_NAME)...."
	NORTHSTAR_LOG_IGNORE=1 go test -benchmem -run=. -bench=. ./pkg/backend

prof:
	@echo -e "\n....Profiling $(GO_PROJECT_NAME)...."
	NORTHSTAR_LOG_IGNORE=1 go test -cpuprofile cpu.prof -memprofile mem.prof -bench=. ./pkg/backend

clean:
	rm -f ./bin/$(GO_PROJECT_NAME)

# Lint all Go code via golangci-lint (replaces check-format, vet, gosec, staticcheck)
lint:
	@echo "Running golangci-lint..."
	$(_Q)golangci-lint run ./...
	@echo "  golangci-lint ok"

# Auto-fix all linter issues that have fixers
fix:
	golangci-lint run --fix ./...

# Govulncheck — dependency vulnerability scanning (not covered by golangci-lint)
govulncheck:
	@echo "Running govulncheck..."
	$(_Q)go tool govulncheck ./...
	@echo "  govulncheck ok"

# E2E tests using Docker (predastore + northstar)
e2e:
	@echo -e "\n....Running E2E tests...."
	cd e2e && docker compose up -d --build
	@echo "Waiting for services to start..."
	sleep 5
	cd e2e && NORTHSTAR_E2E=1 go test -v -timeout 120s ./...
	cd e2e && docker compose down -v

e2e-down:
	cd e2e && docker compose down -v

# Build and push multi-arch docker image
docker:
	@echo -e "\n....Building and pushing docker image...."
	$(MAKE) test
	docker buildx build --push --platform linux/arm/v7,linux/arm64/v8,linux/amd64 --tag $(DOCKER_IMAGE):latest .

.PHONY: build go_build run preflight test test-cover test-race diff-coverage bench prof clean \
	lint fix govulncheck e2e e2e-down docker
