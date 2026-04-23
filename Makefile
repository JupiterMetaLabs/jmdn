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

.PHONY: all build clean run test fmt lint lint-fix version deploy

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

# Run all unit tests (requires live ImmuDB + seed node for integration tests)
test:
	go test ./...

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
