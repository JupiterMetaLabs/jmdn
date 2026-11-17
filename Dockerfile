# syntax=docker/dockerfile:1
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
# Base images are pinned by digest (multi-arch index) so a rebuilt tag upstream
# cannot change what we ship. To update: pick the new tag, read its index
# digest (docker buildx imagetools inspect <image>:<tag>), replace both here.
# go1.27.4 fixes GO-2026-6859 (net/http MaxBytesReader header race, an HTTP/1
# server crash) — reachable here from gatekeeper.ServeHTTP, rpc.WSServer and
# metrics.StartMetricsServer, i.e. the public RPC surface.
#
# The builder image is what pins the stdlib, NOT the `toolchain` line in go.mod:
# GOTOOLCHAIN=local is set below, so the go command will not switch toolchains
# and silently builds with whatever this image ships. Bumping go.mod alone
# leaves the vulnerable stdlib in the released binary.
#
# Index (multi-arch manifest list) digest, matching the amd64/arm64 matrix in
# docker.yml. Verify with: docker buildx imagetools inspect golang:1.27.4-bookworm
FROM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS builder

# Build only with the pinned image's toolchain; never download another one at
# build time. Bump the base image when go.mod needs a newer Go.
ENV GOTOOLCHAIN=local

# Install build dependencies (CGO_ENABLED=1 requires gcc)
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    gcc \
    git \
    curl \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# JupiterMetaLabs modules are private: fetch them directly (not through the
# public proxy / checksum DB, which cannot see them). go.sum still pins every
# hash.
ENV GOPRIVATE=github.com/JupiterMetaLabs/*

# Copy dependency files first for better layer caching
COPY go.mod go.sum ./
# The read token arrives as a BuildKit secret and is only visible to this RUN:
# it is passed to git through per-command env, so it never lands in a layer or
# in the image's git config. Without the secret, the download runs
# unauthenticated (works only if the modules are public).
RUN --mount=type=secret,id=go_modules_token \
    if [ -s /run/secrets/go_modules_token ]; then \
      export GIT_CONFIG_COUNT=1 \
             GIT_CONFIG_KEY_0="url.https://x-access-token:$(cat /run/secrets/go_modules_token)@github.com/.insteadOf" \
             GIT_CONFIG_VALUE_0="https://github.com/"; \
    fi && \
    go mod download

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
FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc

# Install runtime dependencies + Yggdrasil (required: network.yggdrasil: true in jmdn.yaml)
#
# `apt-get upgrade` STAYS, and the reproducibility argument for removing it does
# not hold. This stage already installs ten packages (libc6, openssl, gnupg, …)
# and yggdrasil from a third-party apt repo with NO version pins, so the image
# is a function of the build clock whether or not the upgrade runs. Dropping it
# would narrow that surface from ~155 packages to ~11 while leaving the property
# it claimed to buy — one commit, one image — still false.
#
# Removing it was proposed on the basis that two replacement controls would take
# over: a nightly image scan telling you when a digest bump is due, and
# Dependabot raising that bump. Neither is in place on the cadence the argument
# assumed, because BOTH are read from the DEFAULT branch (main) and this work is
# on v3base:
#   - dependabot.yml on a non-default branch is never read at all, so no digest
#     bump is ever proposed. (`target-branch` redirects version updates once the
#     file is on main; it does not make a file on v3base take effect.)
#   - nightly.yml's `schedule:` only fires from the default branch. Its
#     `push: branches: [v3base]` trigger DOES work and does build and scan the
#     image — 40 runs to date, every one of them `push`, zero `schedule`
#     (verified against the Actions API, 2026-11-17). So the image is scanned
#     when someone pushes, and not at all during a quiet week, which is exactly
#     when a newly published Debian CVE would go unseen.
# Until the cadence is real, this line is what pulls Debian security fixes into
# the runtime image between digest bumps.
#
# To revisit, in this order: land dependabot.yml and nightly.yml on main so the
# digest bump is proposed and the scan runs daily, pin the package versions
# below, then drop the upgrade.
RUN apt-get update && apt-get upgrade -y --no-install-recommends \
    && apt-get install -y --no-install-recommends \
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

# Create non-root user pinned to UID/GID 3322 — must match JMDN_UID (3322) used
# in bootstrap_sync.sh so the node (started via gosu jmdn) can write to its
# state directory after the snapshot is extracted and chowned to 3322:3322.
RUN groupadd -r -g 3322 jmdn && useradd -r -u 3322 -g jmdn -d /home/jmdn -s /bin/bash -m jmdn

# Create required directories and hand ownership to jmdn (mirrors install_services.sh layout)
# /opt/jmdn          = JMDN_DATA root (WorkingDirectory for jmdn process)
# /opt/jmdn/storage  = ThebeDB embedded store (BadgerDB KV + SQL projection)
# /opt/jmdn/config   = peer.json and other jmdn config
# /opt/jmdn/DB       = gossipnode.db (DBPath = "./DB/gossipnode.db" relative to WorkingDirectory)
# /opt/jmdn/certs    = TLS certs (self-signed or operator-mounted)
RUN mkdir -p \
    /etc/jmdn/certs \
    /opt/jmdn/storage \
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
EXPOSE 15000 15000/udp 8090 15050 15055 15052 8545 8546

# Health check against Explorer API (ports.api).
# API is disabled by default — enable by setting ports.api: 8090 in jmdn.yaml.
# start-period extended to 300s to allow bootstrap sync on first run.
# Auth: Explorer API requires a Bearer token when security.explorer_api_key is set.
# Pass the key via JMDN_SECURITY_EXPLORER_API_KEY env var (docker run -e or compose).
HEALTHCHECK --interval=30s --timeout=5s --start-period=300s --retries=3 \
    CMD sh -c 'curl -sf http://localhost:8090/api/v1/node/version \
        -H "Authorization: Bearer ${JMDN_SECURITY_EXPLORER_API_KEY:-}" || exit 1'

# Volume declaration — all node state under /opt/jmdn
# (ThebeDB storage/, DB/, config/, certs/).
VOLUME ["/opt/jmdn"]

# Entrypoint runs as root so bootstrap_sync can chown the extracted snapshot.
# After bootstrap it uses gosu to drop to jmdn for the node process.
# WORKDIR = /opt/jmdn — matches bare metal WorkingDirectory=${JMDN_DATA}.
# jmdn resolves "./DB/gossipnode.db" and "./config/peer.json" relative to this.
WORKDIR /opt/jmdn

# Startup order: bootstrap (root) → restore paths → gosu jmdn jmdn
# Override config: -v /your/jmdn.yaml:/etc/jmdn/jmdn.yaml
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
# Binary loads /etc/jmdn/jmdn.yaml automatically via viper — no -config flag needed.
# Pass extra flags here to override: CMD ["-seednode", "host:9090"]
CMD []
