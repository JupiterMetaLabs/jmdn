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

.PHONY: all build clean run test fmt lint lint-fix version deploy \
        infra-kv infra-redis infra-sql infra \
        infra-redis-start infra-sql-start infra-sql-setup

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

# ── Infrastructure Setup ──────────────────────────────────────────────────────
# Supports macOS (Homebrew) and Linux (apt). OS is auto-detected.
# Install only — does not start services automatically.
# To start: make infra-redis-start / make infra-sql-start

JMDN_KV_PATH ?= /opt/jmdn/thebe-kv
OS            := $(shell uname -s)

# ── KV (BadgerDB — embedded, no daemon) ───────────────────────────────────────

infra-kv:
	@if [ -d "$(JMDN_KV_PATH)" ]; then \
		echo "✓ KV path already exists: $(JMDN_KV_PATH)"; \
	else \
		echo "→ creating KV directory at $(JMDN_KV_PATH)"; \
		sudo mkdir -p $(JMDN_KV_PATH); \
		sudo chown -R $$(whoami) /opt/jmdn; \
		echo "✓ done — set thebe.kv_path: \"$(JMDN_KV_PATH)\" in jmdn.yaml"; \
	fi

# ── Redis ─────────────────────────────────────────────────────────────────────

infra-redis:
	@if [ "$(OS)" = "Darwin" ]; then \
		if brew list redis &>/dev/null; then \
			echo "✓ Redis already installed"; \
		else \
			echo "→ installing Redis (Homebrew)"; \
			brew install redis; \
			echo "✓ done — run: make infra-redis-start"; \
		fi; \
	elif [ "$(OS)" = "Linux" ]; then \
		if command -v redis-server &>/dev/null; then \
			echo "✓ Redis already installed"; \
		else \
			echo "→ installing Redis (apt)"; \
			sudo apt-get update -qq && sudo apt-get install -y redis-server; \
			echo "✓ done — run: make infra-redis-start"; \
		fi; \
	else \
		echo "✗ unsupported OS: $(OS)"; exit 1; \
	fi

# ── PostgreSQL ────────────────────────────────────────────────────────────────

infra-sql:
	@if [ "$(OS)" = "Darwin" ]; then \
		if brew list postgresql@16 &>/dev/null; then \
			echo "✓ PostgreSQL@16 already installed"; \
		else \
			echo "→ installing PostgreSQL@16 (Homebrew)"; \
			brew install postgresql@16; \
			echo "✓ done — run: make infra-sql-start"; \
		fi; \
	elif [ "$(OS)" = "Linux" ]; then \
		if command -v psql &>/dev/null; then \
			echo "✓ PostgreSQL already installed"; \
		else \
			echo "→ installing PostgreSQL (apt)"; \
			sudo apt-get update -qq && sudo apt-get install -y postgresql postgresql-client; \
			echo "✓ done — run: make infra-sql-start"; \
		fi; \
	else \
		echo "✗ unsupported OS: $(OS)"; exit 1; \
	fi

# ── Install all three ─────────────────────────────────────────────────────────

infra: infra-kv infra-redis infra-sql
	@echo ""
	@echo "✓ all infra installed (OS=$(OS))"
	@echo ""
	@echo "Next:"
	@echo "  make infra-redis-start"
	@echo "  make infra-sql-start    (then: make infra-sql-setup)"
	@echo ""
	@echo "jmdn.yaml:"
	@echo "  thebe:"
	@echo "    enabled: true"
	@echo "    kv_path: \"$(JMDN_KV_PATH)\""
	@echo "    redis_url: \"redis://127.0.0.1:6379\""
	@echo "  env: THEBE_SQL_DSN=postgres://jmdn@localhost:5432/jmdn?sslmode=disable"

# ── Start (foreground) ────────────────────────────────────────────────────────

infra-redis-start:
	@echo "→ starting Redis on 127.0.0.1:6379 (ctrl-c to stop)"
	@if [ "$(OS)" = "Darwin" ]; then \
		redis-server --daemonize no; \
	else \
		redis-server --daemonize no; \
	fi

infra-sql-start:
	@echo "→ starting PostgreSQL (ctrl-c to stop)"
	@if [ "$(OS)" = "Darwin" ]; then \
		/opt/homebrew/opt/postgresql@16/bin/postgres -D /opt/homebrew/var/postgresql@16; \
	else \
		PG_VER=$$(pg_lsclusters -h | awk '{print $$1}' | head -1); \
		PG_CLUSTER=$$(pg_lsclusters -h | awk '{print $$2}' | head -1); \
		PG_DATA=$$(pg_lsclusters -h | awk '{print $$6}' | head -1); \
		if [ ! -f "$$PG_DATA/postgresql.conf" ]; then \
			echo "→ cluster not initialised — running pg_createcluster $$PG_VER $$PG_CLUSTER"; \
			sudo pg_createcluster $$PG_VER $$PG_CLUSTER; \
		fi; \
		echo "  cluster: $$PG_VER/$$PG_CLUSTER  data: $$PG_DATA"; \
		sudo pg_ctlcluster $$PG_VER $$PG_CLUSTER start; \
	fi

# Create jmdn db + user — run once after the first infra-sql-start.
infra-sql-setup:
	@echo "→ setting up jmdn database"
	@if [ "$(OS)" = "Darwin" ]; then \
		/opt/homebrew/opt/postgresql@16/bin/createuser --superuser jmdn 2>/dev/null || echo "  user jmdn already exists"; \
		/opt/homebrew/opt/postgresql@16/bin/createdb --owner=jmdn jmdn 2>/dev/null || echo "  database jmdn already exists"; \
	else \
		sudo -u postgres createuser --superuser jmdn 2>/dev/null || echo "  user jmdn already exists"; \
		sudo -u postgres createdb --owner=jmdn jmdn 2>/dev/null   || echo "  database jmdn already exists"; \
	fi
	@echo "✓ DSN: postgres://jmdn@localhost:5432/jmdn?sslmode=disable"

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
