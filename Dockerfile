# =============================================================================
# JMDN (Jupiter MetaZK Decentralized Network) - Multi-stage Dockerfile
# =============================================================================
# Build:   docker build -t ghcr.io/jupitermetalabs/jmdn:latest .
# Run:     docker run -d --name jmdn \
#            -v $(pwd)/jmdn.yaml:/etc/jmdn/jmdn.yaml:ro \
#            -v jmdn-data:/opt/jmdn \
#            -p 15000:15000 -p 15000:15000/udp -p 8545:8545 -p 8546:8546 \
#            ghcr.io/jupitermetalabs/jmdn:latest
# Docs:    See DOCKER.md § 5 for full docker run reference
# =============================================================================

# -----------------------------------------------------------------------------
# Stage 1: Build
# -----------------------------------------------------------------------------
FROM golang:1.25.3-bookworm AS builder

# Install build dependencies (CGO_ENABLED=1 requires gcc)
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    gcc \
    git \
    curl \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Copy dependency files first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary with version info embedded
ARG GIT_COMMIT=""
ARG GIT_BRANCH=""
ARG GIT_TAG=""

RUN GIT_COMMIT=${GIT_COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")} && \
    GIT_BRANCH=${GIT_BRANCH:-$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")} && \
    GIT_TAG=${GIT_TAG:-$(git describe --tags --always --dirty 2>/dev/null || echo "unknown")} && \
    BUILD_TIME=$(date -u '+%Y-%m-%d_%H:%M:%S') && \
    CGO_ENABLED=1 go build \
    -ldflags "-X 'gossipnode/config/version.gitCommit=${GIT_COMMIT}' \
              -X 'gossipnode/config/version.gitBranch=${GIT_BRANCH}' \
              -X 'gossipnode/config/version.gitTag=${GIT_TAG}' \
              -X 'gossipnode/config/version.buildTime=${BUILD_TIME}' \
              -linkmode=external -w -s" \
    -o /src/jmdn .

# -----------------------------------------------------------------------------
# Stage 2: Runtime
# -----------------------------------------------------------------------------
FROM debian:bookworm-slim

# Install runtime dependencies + Yggdrasil (required: network.yggdrasil: true in jmdn.yaml)
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    gnupg \
    gosu \
    libc6 \
    netcat-openbsd \
    wget \
    openssl \
    python3 \
    gawk \
    && mkdir -p /usr/local/apt-keys \
    && gpg --fetch-keys https://neilalexander.s3.dualstack.eu-west-2.amazonaws.com/deb/key.txt \
    && gpg --export 1C5162E133015D81A811239D1840CDAC6011C5EA \
       | tee /usr/local/apt-keys/yggdrasil-keyring.gpg > /dev/null \
    && echo 'deb [signed-by=/usr/local/apt-keys/yggdrasil-keyring.gpg] http://neilalexander.s3.dualstack.eu-west-2.amazonaws.com/deb/ debian yggdrasil' \
       > /etc/apt/sources.list.d/yggdrasil.list \
    && apt-get update && apt-get install -y --no-install-recommends \
       yggdrasil \
    && rm -rf /var/lib/apt/lists/*

# Install ImmuDB
ARG IMMUDB_VERSION=1.10.0
ARG TARGETARCH
RUN ARCH=$([ "$TARGETARCH" = "arm64" ] && echo "arm64" || echo "amd64") && \
    curl -fsSL "https://github.com/codenotary/immudb/releases/download/v${IMMUDB_VERSION}/immudb-v${IMMUDB_VERSION}-linux-${ARCH}" \
    -o /usr/local/bin/immudb && \
    chmod +x /usr/local/bin/immudb

# Create non-root user pinned to UID/GID 3322 — must match IMMUDB_UID (3322) used
# in bootstrap_sync.sh so that immudb (started via gosu jmdn) can write to its
# data directory after the snapshot is extracted and chowned to 3322:3322.
RUN groupadd -r -g 3322 jmdn && useradd -r -u 3322 -g jmdn -d /home/jmdn -s /bin/bash -m jmdn

# Create required directories and hand ownership to jmdn (mirrors install_services.sh layout)
# /opt/jmdn          = JMDN_DATA root (WorkingDirectory for jmdn process)
# /opt/jmdn/data     = immudb --dir (systemdb, defaultdb, accountsdb)
# /opt/jmdn/config   = peer.json and other jmdn config
# /opt/jmdn/DB       = gossipnode.db (DBPath = "./DB/gossipnode.db" relative to WorkingDirectory)
# /opt/jmdn/certs    = TLS certs (self-signed or operator-mounted)
RUN mkdir -p \
    /etc/jmdn/certs \
    /opt/jmdn/data \
    /opt/jmdn/config \
    /opt/jmdn/DB \
    /opt/jmdn/certs \
    /var/log/jmdn \
    && chown -R jmdn:jmdn /opt/jmdn /var/log/jmdn /etc/jmdn

# Copy binary and scripts from builder
COPY --from=builder /src/jmdn                           /usr/local/bin/jmdn
COPY --from=builder /src/Scripts/start_jmdn_wrapper.sh  /usr/local/bin/start_jmdn_wrapper.sh
COPY --from=builder /src/Scripts/docker-entrypoint.sh   /usr/local/bin/docker-entrypoint.sh
COPY --from=builder /src/Scripts/bootstrap_sync.sh      /usr/local/bin/bootstrap_sync.sh
RUN chmod +x /usr/local/bin/start_jmdn_wrapper.sh \
             /usr/local/bin/docker-entrypoint.sh \
             /usr/local/bin/bootstrap_sync.sh

# No config is baked into the image. Operators mount their own jmdn.yaml via
# docker-compose.yml: ./jmdn.yaml → /etc/jmdn/jmdn.yaml (see DOCKER.md Step 3).
# If no file is mounted, the node uses viper's programmatic defaults (defaults.go).

# peer.json is generated by the node on first run — not baked into the image.
# docker-entrypoint.sh creates /opt/jmdn/data/config/ if missing and lets
# the node generate its own peer identity.

# Expose ports per jmdn_default.yaml (localhost-bound ports excluded)
# 15000 - P2P gossip (LibP2P)      TCP + UDP/QUIC    must be public — see PORTS.md
# 15001 - Yggdrasil direct-msg     TCP only          not wired up in this image yet — see PORTS.md
# 8090  - Explorer API             (ports.api)       disabled by default; set ports.api: 8090 in jmdn.yaml
# 15050 - Block generation         (ports.blockgen)  not exposed by default — see PORTS.md
# 15055 - Block propagation gRPC   (ports.blockgrpc) not exposed by default — see PORTS.md
# 15052 - DID service              (ports.did)       not exposed by default — RegisterDID has no auth, see PORTS.md
# 8545  - Facade / JSON-RPC        (ports.facade)
# 8546  - WebSocket                (ports.ws)
# ImmuDB (3322) is container-internal — not exposed
EXPOSE 15000 15000/udp 8090 8545 8546

# Health check against Explorer API (ports.api).
# API is disabled by default — enable by setting ports.api: 8090 in jmdn.yaml.
# start-period extended to 300s to allow bootstrap sync on first run.
# Auth: Explorer API requires a Bearer token when security.explorer_api_key is set.
# Pass the key via JMDN_SECURITY_EXPLORER_API_KEY env var (docker run -e or compose).
HEALTHCHECK --interval=30s --timeout=5s --start-period=300s --retries=3 \
    CMD sh -c 'curl -sf http://localhost:8090/api/v1/node/version \
        -H "Authorization: Bearer ${JMDN_SECURITY_EXPLORER_API_KEY:-}" || exit 1'

# Volume declaration — all node state under /opt/jmdn.
# compose (IMMUDB_EXTERNAL=true): immudb-data is mounted separately into the immudb
#   container at /opt/jmdn/data — jmdn container only uses jmdn-state for DB/, config/, certs/.
# embedded (IMMUDB_EXTERNAL=false, docker run): single volume holds everything
#   including /opt/jmdn/data (immudb files) and /opt/jmdn/DB, config/, certs/.
VOLUME ["/opt/jmdn"]

# Entrypoint runs as root so bootstrap_sync can chown the extracted snapshot.
# After bootstrap it uses gosu to drop to jmdn for the node process.
# WORKDIR = /opt/jmdn — matches bare metal WorkingDirectory=${JMDN_DATA}.
# jmdn resolves "./DB/gossipnode.db" and "./config/peer.json" relative to this.
WORKDIR /opt/jmdn

# Startup order: bootstrap (root) → restore paths → gosu jmdn immudb → gosu jmdn jmdn
# Override config: -v /your/jmdn.yaml:/etc/jmdn/jmdn.yaml
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
# Binary loads /etc/jmdn/jmdn.yaml automatically via viper — no -config flag needed.
# Pass extra flags here to override: CMD ["-seednode", "host:9090"]
CMD []
