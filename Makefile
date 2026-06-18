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
        infra-redis-start infra-sql-start infra-sql-setup infra-thebe-reset

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
#
# NOTE: These variables are INFRA-ONLY — they are used exclusively by the
# infra-* targets below to provision directories and databases on this machine.
# They are NOT passed to the Go build and are NOT baked into the binary.
# The running node reads all ThebeDB config at runtime from jmdn.yaml:
#   thebe.kv_path, thebe.sql_dsn (or env THEBE_SQL_DSN), thebe.redis_url.

JMDN_KV_PATH      ?= /opt/jmdn/thebe-kv
JMDN_PG_PORT      ?= 5430
JMDN_PG_HOST      ?= 0.0.0.0
JMDN_PG_PASSWORD  ?= jmdndefault
JMDN_SERVICE_USER ?= jmdn
OS                := $(shell uname -s)

# ── KV (BadgerDB — embedded, no daemon) ───────────────────────────────────────

infra-kv:
	@if [ -d "$(JMDN_KV_PATH)" ]; then \
		echo "✓ KV path already exists: $(JMDN_KV_PATH)"; \
	else \
		echo "→ creating KV directory at $(JMDN_KV_PATH)"; \
		sudo mkdir -p $(JMDN_KV_PATH); \
		sudo chown -R $(JMDN_SERVICE_USER) /opt/jmdn; \
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
	@echo "  env: THEBE_SQL_DSN=postgres://jmdn:$(JMDN_PG_PASSWORD)@$(JMDN_PG_HOST):$(JMDN_PG_PORT)/jmdn?sslmode=disable"

# ── Start (foreground) ────────────────────────────────────────────────────────

infra-redis-start:
	@echo "→ starting Redis on 127.0.0.1:6379 (ctrl-c to stop)"
	@if [ "$(OS)" = "Darwin" ]; then \
		redis-server --daemonize no; \
	else \
		redis-server --daemonize no; \
	fi

infra-sql-start:
	@echo "→ starting PostgreSQL on $(JMDN_PG_HOST):$(JMDN_PG_PORT) (ctrl-c to stop)"
	@if [ "$(OS)" = "Darwin" ]; then \
		/opt/homebrew/opt/postgresql@16/bin/postgres \
			-D /opt/homebrew/var/postgresql@16 \
			-p $(JMDN_PG_PORT) \
			-h $(JMDN_PG_HOST); \
	else \
		PG_VER=$$(pg_lsclusters -h 2>/dev/null | awk '{print $$1}' | head -1); \
		PG_CLUSTER=$$(pg_lsclusters -h 2>/dev/null | awk '{print $$2}' | head -1); \
		if [ -z "$$PG_VER" ]; then \
			PG_VER=$$(ls /usr/lib/postgresql/ 2>/dev/null | sort -V | tail -1); \
		fi; \
		if [ -z "$$PG_CLUSTER" ]; then \
			PG_CLUSTER=main; \
		fi; \
		PG_DATA=/var/lib/postgresql/$$PG_VER/$$PG_CLUSTER; \
		PG_CONF_DIR=/etc/postgresql/$$PG_VER/$$PG_CLUSTER; \
		if [ ! -f "$$PG_DATA/PG_VERSION" ]; then \
			echo "→ creating cluster $$PG_VER/$$PG_CLUSTER on port $(JMDN_PG_PORT)"; \
			sudo pg_dropcluster $$PG_VER $$PG_CLUSTER 2>/dev/null || true; \
			sudo pg_createcluster -p $(JMDN_PG_PORT) $$PG_VER $$PG_CLUSTER; \
		fi; \
		echo "→ setting port=$(JMDN_PG_PORT) listen_addresses='$(JMDN_PG_HOST)' in $$PG_CONF_DIR/postgresql.conf"; \
		sudo sed -i "s/^#*port = .*/port = $(JMDN_PG_PORT)/" $$PG_CONF_DIR/postgresql.conf; \
		sudo sed -i "s/^#*listen_addresses = .*/listen_addresses = '$(JMDN_PG_HOST)'/" $$PG_CONF_DIR/postgresql.conf; \
		echo "→ allowing all-host connections in $$PG_CONF_DIR/pg_hba.conf"; \
		grep -q "^host all all 0.0.0.0/0" $$PG_CONF_DIR/pg_hba.conf \
			|| echo "host all all 0.0.0.0/0 trust" | sudo tee -a $$PG_CONF_DIR/pg_hba.conf > /dev/null; \
		echo "  conf: $$PG_CONF_DIR  data: $$PG_DATA  bind: $(JMDN_PG_HOST):$(JMDN_PG_PORT)"; \
		sudo pg_ctlcluster $$PG_VER $$PG_CLUSTER start; \
	fi

# Wipe all ThebeDB state (KV + SQL) and recreate from scratch — use before re-running migration.
infra-thebe-reset:
	@echo "→ wiping KV store at $(JMDN_KV_PATH)"
	sudo rm -rf $(JMDN_KV_PATH)
	sudo mkdir -p $(JMDN_KV_PATH)
	sudo chown -R $(JMDN_SERVICE_USER) $(JMDN_KV_PATH)
	@echo "→ dropping and recreating jmdn database on port $(JMDN_PG_PORT)"
	@if [ "$(OS)" = "Darwin" ]; then \
		/opt/homebrew/opt/postgresql@16/bin/psql -p $(JMDN_PG_PORT) -U postgres -c "DROP DATABASE IF EXISTS jmdn;" postgres; \
		/opt/homebrew/opt/postgresql@16/bin/psql -p $(JMDN_PG_PORT) -U postgres -c "CREATE DATABASE jmdn OWNER jmdn;" postgres; \
		/opt/homebrew/opt/postgresql@16/bin/psql -p $(JMDN_PG_PORT) -U postgres -c "ALTER USER jmdn WITH PASSWORD '$(JMDN_PG_PASSWORD)';" postgres; \
	else \
		sudo -u postgres psql -p $(JMDN_PG_PORT) -c "DROP DATABASE IF EXISTS jmdn;" postgres; \
		sudo -u postgres psql -p $(JMDN_PG_PORT) -c "CREATE DATABASE jmdn OWNER jmdn;" postgres; \
		sudo -u postgres psql -p $(JMDN_PG_PORT) -c "ALTER USER jmdn WITH PASSWORD '$(JMDN_PG_PASSWORD)';" postgres; \
	fi
	@echo "✓ ThebeDB state wiped — ready for fresh migration"
	@echo "  run: go run ./cmd/immudb-to-thebe --thebe-sql-dsn \"postgres://jmdn:$(JMDN_PG_PASSWORD)@$(JMDN_PG_HOST):$(JMDN_PG_PORT)/jmdn?sslmode=disable\""

# Create jmdn db + user — run once after the first infra-sql-start.
infra-sql-setup:
	@echo "→ setting up jmdn database on port $(JMDN_PG_PORT)"
	@if [ "$(OS)" = "Darwin" ]; then \
		/opt/homebrew/opt/postgresql@16/bin/createuser --superuser -p $(JMDN_PG_PORT) jmdn 2>/dev/null || echo "  user jmdn already exists"; \
		/opt/homebrew/opt/postgresql@16/bin/createdb --owner=jmdn -p $(JMDN_PG_PORT) jmdn 2>/dev/null || echo "  database jmdn already exists"; \
		/opt/homebrew/opt/postgresql@16/bin/psql -p $(JMDN_PG_PORT) -U postgres -c "ALTER USER jmdn WITH PASSWORD '$(JMDN_PG_PASSWORD)';" jmdn; \
	else \
		sudo -u postgres createuser --superuser -p $(JMDN_PG_PORT) jmdn 2>/dev/null || echo "  user jmdn already exists"; \
		sudo -u postgres createdb --owner=jmdn -p $(JMDN_PG_PORT) jmdn 2>/dev/null   || echo "  database jmdn already exists"; \
		sudo -u postgres psql -p $(JMDN_PG_PORT) -c "ALTER USER jmdn WITH PASSWORD '$(JMDN_PG_PASSWORD)';"; \
	fi
	@echo "✓ DSN: postgres://jmdn:$(JMDN_PG_PASSWORD)@$(JMDN_PG_HOST):$(JMDN_PG_PORT)/jmdn?sslmode=disable"

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
