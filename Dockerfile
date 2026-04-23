# =============================================================================
# JMDN (Jupiter MetaZK Decentralized Network) - Multi-stage Dockerfile
# =============================================================================
# Build:   docker build -t jmdn:latest .
# Run:     docker run -d --name jmdn -p 8080:8080 -p 4001:4001 jmdn:latest
# Config:  docker run -d -v /path/to/config.env:/etc/jmdn/config.env jmdn:latest
# =============================================================================

# -----------------------------------------------------------------------------
# Stage 1: Build
# -----------------------------------------------------------------------------
FROM golang:1.25-bookworm AS builder

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

# Install runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    libc6 \
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

# Create required directories
RUN mkdir -p /etc/jmdn /opt/jmdn/data /var/log/jmdn && \
    chown -R jmdn:jmdn /opt/jmdn /var/log/jmdn /etc/jmdn

# Copy binary and default config from builder
COPY --from=builder /src/jmdn /usr/local/bin/jmdn
COPY --from=builder /src/jmdn_default.yaml /etc/jmdn/jmdn_default.yaml
COPY --from=builder /src/config/peer.json /etc/jmdn/peer.json

# Expose common ports
# 8080 - HTTP API / Explorer
# 4001 - P2P / libp2p
# 3323 - ImmuDB
# 50051 - gRPC
EXPOSE 8080 4001 3323 50051

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

# Data volume for ImmuDB persistence
VOLUME ["/opt/jmdn/data"]

USER jmdn
WORKDIR /home/jmdn

# Default entrypoint - override config with -v /your/config.env:/etc/jmdn/config.env
ENTRYPOINT ["jmdn"]
CMD ["-config", "/etc/jmdn/config.env"]
