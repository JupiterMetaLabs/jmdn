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
ARG YGGDRASIL_VERSION=0.5.12
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    gnupg \
    libc6 \
    && mkdir -p /usr/local/apt-keys \
    && curl -fsSL https://neilalexander.s3.dualstack.eu-west-2.amazonaws.com/deb/key.txt \
       | gpg --dearmor --yes -o /usr/local/apt-keys/yggdrasil-keyring.gpg \
    && echo 'deb [signed-by=/usr/local/apt-keys/yggdrasil-keyring.gpg] http://neilalexander.s3.dualstack.eu-west-2.amazonaws.com/deb/ debian yggdrasil' \
       > /etc/apt/sources.list.d/yggdrasil.list \
    && apt-get update && apt-get install -y --no-install-recommends \
       yggdrasil=${YGGDRASIL_VERSION} \
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

# Copy binary, wrapper, and default config from builder
COPY --from=builder /src/jmdn /usr/local/bin/jmdn
COPY --from=builder /src/Scripts/start_jmdn_wrapper.sh /usr/local/bin/start_jmdn_wrapper.sh
RUN chmod +x /usr/local/bin/start_jmdn_wrapper.sh
# Copy default config as jmdn.yaml (mirrors setup_config.sh: cp jmdn_default.yaml → jmdn.yaml)
COPY --from=builder /src/jmdn_default.yaml /etc/jmdn/jmdn.yaml
COPY --from=builder /src/config/peer.json /etc/jmdn/peer.json

# Expose ports per jmdn.yaml (localhost-bound ports excluded)
# 6090  - HTTP API / Explorer      (ports.api)
# 16050 - Block generation         (ports.blockgen)
# 16055 - Block propagation gRPC   (ports.blockgrpc)
# 16052 - DID service              (ports.did)
# 6545  - Facade / JSON-RPC        (ports.facade)
# 6546  - WebSocket                (ports.ws)
# 3323  - ImmuDB
EXPOSE 6090 16050 16055 16052 6545 6546 3323

# Health check against actual API port (ports.api: 6090)
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:6090/health || exit 1

# Data volume for ImmuDB + node persistence
VOLUME ["/opt/jmdn/data"]

USER jmdn
WORKDIR /home/jmdn

# Wrapper handles binary path resolution; override config with:
#   -v /your/jmdn.yaml:/etc/jmdn/jmdn.yaml
ENTRYPOINT ["/usr/local/bin/start_jmdn_wrapper.sh"]
CMD ["-config", "/etc/jmdn/jmdn.yaml"]
