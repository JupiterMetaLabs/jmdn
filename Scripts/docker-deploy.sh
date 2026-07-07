#!/usr/bin/env bash
################################################################################
# docker-deploy.sh — Atomic Upgrade & Auto-Rollback for the JMDN Docker Stack
#
# Docker equivalent of Scripts/deploy.sh. Same contract, different swap unit:
#
#   deploy.sh (bare metal)              docker-deploy.sh (this script)
#   ----------------------------------  ----------------------------------
#   build new binary                    docker compose pull jmdn
#   cp current binary -> jmdn.bak       docker tag current image -> jmdn-rollback:pre-upgrade
#   mv new binary into place            docker compose up -d jmdn
#   systemctl restart + health-poll     docker inspect .State.Health.Status poll
#   restore .bak + restart on failure   docker tag jmdn-rollback:pre-upgrade -> current ref,
#                                        docker compose up -d jmdn again
#
# What this does NOT do (by design, matches DOCKER.md §13 Option A):
#   - It does not choose which version to deploy. Pin the release first by
#     setting JMDN_VERSION in .env (see DOCKER.md §13), then run this script
#     instead of the manual pull/up commands there.
#   - It does not touch immudb or redis. Those upgrade independently
#     (DOCKER.md §13 "Upgrading Redis or ImmuDB") — a jmdn-only rollback
#     can't un-upgrade them anyway, so scope is kept to what this script
#     can actually make safe.
#
# Usage:
#   ./Scripts/docker-deploy.sh
#
# Exit codes: 0 = deployed and healthy. 1 = failed (rolled back if a
# previous image existed; left in whatever state the rollback attempt
# produced otherwise — check `docker compose logs jmdn`).
################################################################################

set -euo pipefail

# --- Logging (self-contained by design — see start_jmdn_wrapper.sh's header
#     for the rationale: this script may run from CI/Ansible far from the
#     repo, so it doesn't depend on sourcing lib/platform.sh) -----------------
COLOR_RED='\033[0;31m'; COLOR_GREEN='\033[0;32m'; COLOR_YELLOW='\033[1;33m'; COLOR_BLUE='\033[0;34m'; COLOR_NC='\033[0m'
log_info()  { echo -e "${COLOR_BLUE}[INFO]${COLOR_NC}  $*"; }
log_ok()    { echo -e "${COLOR_GREEN}[OK]${COLOR_NC}    $*"; }
log_warn()  { echo -e "${COLOR_YELLOW}[WARN]${COLOR_NC}  $*" >&2; }
log_error() { echo -e "${COLOR_RED}[ERROR]${COLOR_NC} $*" >&2; }

# --- Config -------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
SERVICE="jmdn"
ROLLBACK_TAG="jmdn-rollback:pre-upgrade"

# jmdn's own healthcheck has start_period: 300s (bootstrap/cold-start can be
# slow) — polling like bare-metal's 10x3s would false-negative on every
# normal deploy. Give it room to actually pass or fail on its own terms.
HEALTH_RETRIES="${HEALTH_RETRIES:-40}"
HEALTH_DELAY="${HEALTH_DELAY:-10}"   # 40 * 10s = ~6.5 min ceiling

cd "$PROJECT_DIR"
log_info "Working directory: $(pwd)"

# --- Preflight ------------------------------------------------------------
command -v docker >/dev/null 2>&1 || { log_error "docker not found on PATH."; exit 1; }
docker compose version >/dev/null 2>&1 || { log_error "docker compose v2 not found."; exit 1; }
[ -f docker-compose.yml ] || { log_error "docker-compose.yml not found in $(pwd)."; exit 1; }

# --- Snapshot current state before touching anything -----------------------
# CURRENT_IMAGE_ID is the content-addressed image sha — stable even though
# CURRENT_IMAGE_REF (a mutable tag like ghcr.io/.../jmdn:latest) is about to
# be repointed by `docker compose pull`. Tagging the ID locally is what makes
# rollback possible: the tag pull just moved doesn't remember what it used
# to point to, but this local tag does.
CURRENT_CONTAINER_EXISTS=false
CURRENT_IMAGE_ID=""
CURRENT_IMAGE_REF=""

if docker inspect "$SERVICE" >/dev/null 2>&1; then
    CURRENT_CONTAINER_EXISTS=true
    CURRENT_IMAGE_ID=$(docker inspect --format='{{.Image}}' "$SERVICE")
    CURRENT_IMAGE_REF=$(docker inspect --format='{{.Config.Image}}' "$SERVICE")
    log_info "Current running image: ${CURRENT_IMAGE_REF} (${CURRENT_IMAGE_ID:0:19})"

    docker tag "$CURRENT_IMAGE_ID" "$ROLLBACK_TAG"
    log_ok "Snapshotted current image -> ${ROLLBACK_TAG} (rollback target if this deploy fails)"
else
    log_warn "No running '${SERVICE}' container found — this looks like a first deploy."
    log_warn "No rollback safety net will be available if it fails."
fi

# --- Pull + recreate ---------------------------------------------------------
log_info "Pulling latest image for ${SERVICE}..."
docker compose pull "$SERVICE"

log_info "Recreating ${SERVICE}..."
docker compose up -d "$SERVICE"

# --- Health poll --------------------------------------------------------------
# Mirrors deploy.sh's health-poll-then-rollback loop, but reads Docker's own
# HEALTHCHECK status instead of systemd unit state. Falls back to "is it
# running at all" if the container has no healthcheck defined (e.g. someone
# stripped it from docker-compose.yml) so this script degrades gracefully
# instead of hanging forever waiting for a status that will never appear.
poll_health() {
    local i status
    for i in $(seq 1 "$HEALTH_RETRIES"); do
        status=$(docker inspect --format='{{if .State.Health}}{{.State.Health.Status}}{{else}}no-healthcheck{{end}}' "$SERVICE" 2>/dev/null || echo "gone")

        case "$status" in
            healthy)
                log_ok "Container is healthy (attempt $i/${HEALTH_RETRIES})."
                return 0
                ;;
            no-healthcheck)
                # No HEALTHCHECK configured at all — best we can do is confirm
                # it's still running and hasn't immediately crash-looped.
                if [ "$(docker inspect --format='{{.State.Running}}' "$SERVICE" 2>/dev/null)" = "true" ]; then
                    log_warn "No healthcheck defined on '${SERVICE}' — confirmed running, not confirmed healthy."
                    return 0
                fi
                ;;
            unhealthy)
                log_error "Container reported unhealthy (attempt $i/${HEALTH_RETRIES})."
                return 1
                ;;
            gone)
                log_error "Container '${SERVICE}' is not running (attempt $i/${HEALTH_RETRIES})."
                return 1
                ;;
            *)
                log_warn "Health status: ${status} (attempt $i/${HEALTH_RETRIES}) — waiting..."
                ;;
        esac
        sleep "$HEALTH_DELAY"
    done
    log_error "Timed out after $((HEALTH_RETRIES * HEALTH_DELAY))s waiting for '${SERVICE}' to become healthy."
    return 1
}

if poll_health; then
    log_ok "Deployment complete — ${SERVICE} is up and healthy."
    # Keep exactly one rollback snapshot, same retention model as deploy.sh's
    # single .bak file — not deleting it here on purpose, next deploy's
    # `docker tag` overwrites it. Nothing to clean up.
    exit 0
fi

# --- Rollback -----------------------------------------------------------------
log_error "Deployment failed health checks."

if [ "$CURRENT_CONTAINER_EXISTS" = true ]; then
    log_warn "Rolling back ${SERVICE} to previous image (${CURRENT_IMAGE_REF})..."
    docker tag "$ROLLBACK_TAG" "$CURRENT_IMAGE_REF"
    docker compose up -d "$SERVICE"

    # Shorter poll for the rollback itself — if the *previous* image can't
    # come back healthy either, something else on the host changed
    # (config, immudb, disk) and no amount of retrying will fix that here.
    HEALTH_RETRIES=10
    if poll_health; then
        log_ok "Rollback successful — ${SERVICE} is running on the previous image."
    else
        log_error "Rollback failed too! Manual intervention required."
        log_error "Previous image is still tagged at: ${ROLLBACK_TAG}"
    fi
else
    log_error "No previous image available — this was the first deploy. Nothing to roll back to."
fi

log_info "Recent ${SERVICE} logs:"
docker compose logs --tail=50 "$SERVICE" || true

exit 1
