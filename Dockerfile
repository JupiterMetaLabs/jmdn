# =============================================================================
# JMDN (Jupiter MetaZK Decentralized Network) - Multi-stage Dockerfile
# =============================================================================
# Build:   docker build -t jmdn:latest .
# Run:     docker run -d --name jmdn -p 6090:6090 -p 6545:6545 jmdn:latest
# Config:  docker run -d -v /path/to/jmdn.yaml:/etc/jmdn/jmdn.yaml jmdn:latest
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
    libc6 \
    && mkdir -p /usr/local/apt-keys \
    && gpg --fetch-keys https://neilalexander.s3.dualstack.eu-west-2.amazonaws.com/deb/key.txt \
    && gpg --export 1C5162E133015D81A811239D1840CDAC6011C5EA \
       | tee /usr/local/apt-keys/yggdrasil-keyring.gpg > /dev/null \
    && echo 'deb [signed-by=/usr/local/apt-keys/yggdrasil-keyring.gpg] http://neilalexander.s3.dualstack.eu-west-2.amazonaws.com/deb/ debian yggdrasil' \
       > /etc/apt/sources.list.d/yggdrasil.list \
    && apt-get update && apt-get install -y --no-install-recommends \
       yggdrasil \
       netcat-openbsd \
       wget \
       bzip2 \
       openssl \
    && rm -rf /var/lib/apt/lists/*

# Install ImmuDB
ARG IMMUDB_VERSION=1.10.0
ARG TARGETARCH
RUN ARCH=$([ "$TARGETARCH" = "arm64" ] && echo "arm64" || echo "amd64") && \
    curl -fsSL "https://github.com/codenotary/immudb/releases/download/v${IMMUDB_VERSION}/immudb-v${IMMUDB_VERSION}-linux-${ARCH}" \
    -o /usr/local/bin/immudb && \
    chmod +x /usr/local/bin/immudb

# Create non-root user
RUN groupadd -r jmdn && useradd -r -g jmdn -d /home/jmdn -s /bin/bash -m jmdn

# Create required directories (mirrors install_services.sh layout)
RUN mkdir -p \
    /etc/jmdn/certs \
    /opt/jmdn/data/data \
    /opt/jmdn/data/config \
    /opt/jmdn/data/DB \
    /var/log/jmdn \
    && chown -R jmdn:jmdn /opt/jmdn /var/log/jmdn /etc/jmdn

# Copy binary, scripts, and default config from builder
COPY --from=builder /src/jmdn /usr/local/bin/jmdn
COPY --from=builder /src/Scripts/start_jmdn_wrapper.sh /usr/local/bin/start_jmdn_wrapper.sh
COPY --from=builder /src/Scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
COPY --from=builder /src/Scripts/bootstrap_sync.sh    /usr/local/bin/bootstrap_sync.sh
RUN chmod +x /usr/local/bin/start_jmdn_wrapper.sh \
             /usr/local/bin/docker-entrypoint.sh \
             /usr/local/bin/bootstrap_sync.sh
# Copy default config as jmdn.yaml (mirrors setup_config.sh: cp jmdn_default.yaml → jmdn.yaml)
COPY --from=builder /src/jmdn_default.yaml /etc/jmdn/jmdn.yaml
# peer.json must be at ./config/peer.json relative to WORKDIR (hardcoded in config/constants.go)
# WORKDIR is /opt/jmdn/data (volume) so it persists across restarts.
# Also kept at /etc/jmdn/peer.json as a fallback — bootstrap_sync wipes the volume,
# so the entrypoint restores from /etc/jmdn/peer.json if missing after bootstrap.
COPY --from=builder /src/config/peer.json /opt/jmdn/data/config/peer.json
COPY --from=builder /src/config/peer.json /etc/jmdn/peer.json

# Expose ports per jmdn.yaml (localhost-bound ports excluded)
# 6090  - HTTP API / Explorer      (ports.api)
# 16050 - Block generation         (ports.blockgen)
# 16055 - Block propagation gRPC   (ports.blockgrpc)
# 16052 - DID service              (ports.did)
# 6545  - Facade / JSON-RPC        (ports.facade)
# 6546  - WebSocket                (ports.ws)
# ImmuDB (3322) is container-internal — not exposed
EXPOSE 6090 16050 16055 16052 6545 6546

# Health check against actual API port (ports.api: 6090)
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:6090/health || exit 1

# Data volume for ImmuDB + node persistence
VOLUME ["/opt/jmdn/data"]

# Run as root — required for bootstrap_sync.sh (chown after snapshot extract)
# WORKDIR matches where jmdn resolves ./config/peer.json (config/constants.go: PeerFile = "./config/peer.json")
WORKDIR /opt/jmdn/data

# 1. bootstrap_sync.sh  (first run only — downloads snapshot, writes sentinel)
# 2. immudb             (starts in background)
# 3. start_jmdn_wrapper.sh → jmdn
# Override config: -v /your/jmdn.yaml:/etc/jmdn/jmdn.yaml
CMD ["/usr/local/bin/docker-entrypoint.sh"]