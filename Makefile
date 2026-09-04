# Makefile for JMDN

# Binary name (can be overridden via command line: make BINARY_NAME=custom_name)
BINARY_NAME ?= jmdn

# Install path (can be overridden via command line: make deploy INSTALL_PATH=/usr/local/bin)
INSTALL_PATH ?= /usr/local/bin

# Version info
GIT_COMMIT=$(shell git rev-parse --short HEAD)
GIT_BRANCH=$(shell git rev-parse --abbrev-ref HEAD)
GIT_TAG=$(shell git describe --tags --always --dirty | tr -d '`' 2>/dev/null || echo "unknown")
BUILD_TIME=$(shell date -u '+%Y-%m-%d_%H:%M:%S')

# Linker flags
LDFLAGS=-ldflags "-X 'gossipnode/config/version.gitCommit=${GIT_COMMIT}' -X 'gossipnode/config/version.gitBranch=${GIT_BRANCH}' -X 'gossipnode/config/version.gitTag=${GIT_TAG}' -X 'gossipnode/config/version.buildTime=${BUILD_TIME}' -linkmode=external -w -s"

.PHONY: all build clean run test test-unit fmt fmt-check lint lint-new version deploy \
        dev-setup dev-check verify-pins

all: build

build:
	@echo "Building ${BINARY_NAME}..."
	@echo "Version: ${GIT_TAG} (${GIT_COMMIT}) on ${GIT_BRANCH}"
	CGO_ENABLED=1 go build ${LDFLAGS} -o ${BINARY_NAME} .

clean:
	@echo "Cleaning..."
	go clean
	rm -f ${BINARY_NAME}

run: build
	./${BINARY_NAME}

version:
	@echo "Git Tag:    ${GIT_TAG}"
	@echo "Git Commit: ${GIT_COMMIT}"
	@echo "Git Branch: ${GIT_BRANCH}"
	@echo "Build Time: ${BUILD_TIME}"

deploy: build
	@echo "Deploying ${BINARY_NAME} to ${INSTALL_PATH}..."
	@mkdir -p ${INSTALL_PATH}
	@mv ./${BINARY_NAME} ${INSTALL_PATH}/${BINARY_NAME}
	@echo "Deployment complete: ${INSTALL_PATH}/${BINARY_NAME}"

# ── Developer Quality Targets ─────────────────────────────────────────────────
# These mirror exactly what CI runs. Use before pushing.
# Infrastructure setup has moved to Scripts/install_services.sh.

# Run all unit tests (requires live ThebeDB + seed node for integration tests)
test:
	go test ./...

# Fast unit gate for CI / auto-release — skips integration tests (ImmuDB/seed) via testing.Short().
test-unit:
	go test -short ./...

# Check formatting — exits non-zero if any file needs formatting.
# Fix: run 'make fmt' then commit.
fmt-check:
	@golangci-lint fmt --diff

# Auto-fix formatting in place.
fmt:
	golangci-lint fmt

# Run linters as defined in .golangci.yml (Mode A: full codebase).
lint:
	golangci-lint run

# Run linters on new/changed code only (Mode B: diff vs parent commit).
# Useful when a lint backlog exists and you don't want to be blocked by old violations.
lint-new:
	golangci-lint run --new-from-rev=HEAD~1

# ── Module access (private deps) ──────────────────────────────────────────────
# avc and ThebeDB are PRIVATE repos. Go cannot fetch them through the public
# proxy, so every developer needs GOPRIVATE plus an https->ssh rewrite. Run this
# once per machine. Idempotent; `--undo` reverses it.
dev-setup:
	@./Scripts/dev-setup-modules.sh

# Same checks, changes nothing. Use this when a build fails with
# "could not read Username for 'https://github.com'".
dev-check:
	@./Scripts/dev-setup-modules.sh --check

# Also generate .go.work.local so you can build against sibling checkouts of
# avc / ThebeDB / JMDN-FastSync without editing go.mod. Prints the GOWORK export.
dev-workspace:
	@./Scripts/dev-setup-modules.sh --workspace

# THE PRE-PUSH GATE. Proves the versions pinned in go.mod actually resolve,
# independently of any workspace file or local replace. If you develop with
# GOWORK exported, this is the only thing that catches a broken pin — and it is
# what CI enforces.
verify-pins:
	GOWORK=off go mod verify
	GOWORK=off go build ./...
	@! grep -nE '^[[:space:]]*replace[[:space:]]+\S+[[:space:]]+=>[[:space:]]+\.{1,2}/' go.mod \
	  || { echo "ERROR: local filesystem replace in go.mod — pin a published tag instead"; exit 1; }
	@echo "verify-pins: OK"
