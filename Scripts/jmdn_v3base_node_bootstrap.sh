#!/usr/bin/env bash
# jmdn_v3base_node_bootstrap.sh — one run from a fresh Ubuntu VM to a running
# jmdn v3base node seeded from the canary snapshot in S3.
#
# Before running (once per node):
#   1. S3 read access, detected automatically:
#      - AWS VM: attach the read role (s3:ListBucket + s3:GetObject on the bucket).
#      - GCP VM: nothing on the VM. The script gets the VM service account's Google
#        identity token from the metadata server and exchanges it for the AWS role
#        GCP_AWS_ROLE_ARN (trusts accounts.google.com, limited to the service
#        accounts' unique IDs). No keys on the VM.
#      - Anything else: a links file at /root/jmdn-links.env (see LINKS_FILE).
#   2. Nothing to copy for the config: the fleet template (the canary's yaml with
#      per-node secrets blanked) is downloaded from S3, SSE-KMS encrypted; the read
#      role needs kms:Decrypt on that key. LOCAL_CONFIG=/path overrides it.
#   3. Get the canary's BLS public key as HEX (bls.json stores it as base64). On the canary:
#        python3 -c 'import json,base64;print(base64.b64decode(json.load(open("/opt/jmdn/config/bls.json"))["bls_pub"]).hex())'
#   4. Have a GitHub fine-grained token (Contents: read) for jmdn, avc, ThebeDB,
#      JMDN-FastSync, JMDN_Merkletree. The script asks for it, hidden; it is never
#      written to disk or put on a command line.
#
# Run:
#   sudo bash jmdn_v3base_node_bootstrap.sh
# It asks for the node alias, the canary's BLS key (hex) and an optional reward
# address, then the GitHub token. The config template comes from CONFIG_S3 (or a
# links file); LOCAL_CONFIG=/path uses a local copy instead. Answers are saved
# (0600) for re-runs; NODE_ALIAS, CANARY_BLS_PUB, REWARD_ADDRESS and LOCAL_CONFIG
# set in the environment override the saved answers.
#
# Re-runnable: each finished phase leaves a marker in /var/lib/jmdn-bootstrap and is
# skipped next time. Any failed check stops the run with the reason.

set -euo pipefail

# ---- release and snapshot -------------------------------------------------------
# The jmdn release to build: the release that ships this script. No database
# schema change since the canary build (9031eeb5), so the canary snapshot applies.
JMDN_REF="${JMDN_REF:-v3.0.0-beta.2}"
BUCKET="${BUCKET:-jmdt-v3-snapshots}"
PREFIX="${PREFIX:-jmdn-transfer/canary-22454}"
BUCKET_REGION="${BUCKET_REGION:-us-east-1}"
EXPECT_HEIGHT="${EXPECT_HEIGHT:-22454}"
EXPECT_PGDUMP_SHA="${EXPECT_PGDUMP_SHA:-14de757ab98a853dd710c4a4f13fbf138d29fffc9424d5fff9ac72d170798f36}"
EXPECT_TAR_SHA="${EXPECT_TAR_SHA:-fb4d331d07ad38ce1e04ff86f43730c372c5e8f4a92636f4fcb7ce4f8b992fcd}"
CANARY_PEER_ID="12D3KooWFci5zowS2aWhTPxY89HxqzAZ6gGtr4BEo9dzrCJfPuD2"

# ---- this node --------------------------------------------------------------------
NODE_ALIAS="${NODE_ALIAS:-}"
CANARY_BLS_PUB="${CANARY_BLS_PUB:-}"
REWARD_ADDRESS="${REWARD_ADDRESS:-}"
# Fleet config template (the canary's yaml with per-node secrets blanked), stored
# SSE-KMS encrypted in S3. Downloaded fresh for every run that still needs it and
# shredded once the node config is written. LOCAL_CONFIG=/path uses a local file.
CONFIG_S3="${CONFIG_S3:-s3://jmdt-v3-snapshots/jmdn-config/v3base-node-template.yaml}"
LOCAL_CONFIG="${LOCAL_CONFIG:-}"
# GCP: AWS role assumed with the VM's Google identity (see header, step 1).
GCP_AWS_ROLE_ARN="${GCP_AWS_ROLE_ARN:-arn:aws:iam::419231909261:role/jmdn-gcp-reader-role}"
GCP_AUDIENCE="${GCP_AUDIENCE:-jmdn-s3-reader}"
# Hosts on neither AWS nor GCP: a links file with time-limited HTTPS URLs,
# made on a machine that can read the bucket. Defines CONFIG_URL, PGDUMP_URL,
# TAR_URL, SUMS_URL and HEIGHT_URL. When present, no AWS credentials are used;
# the downloads are still checked against the pinned checksums.
LINKS_FILE="${LINKS_FILE:-/root/jmdn-links.env}"
USE_LINKS=0
if [ -f "$LINKS_FILE" ]; then
  # shellcheck disable=SC1090
  . "$LINKS_FILE"
  for v in CONFIG_URL PGDUMP_URL TAR_URL SUMS_URL HEIGHT_URL; do
    [ -n "${!v:-}" ] || { printf 'FAIL %s missing %s\n' "$LINKS_FILE" "$v" >&2; exit 1; }
  done
  USE_LINKS=1
fi
# aws_ready: AWS credentials from the instance role, else from the GCP VM identity.
aws_ready() {
  aws sts get-caller-identity --region "$BUCKET_REGION" >/dev/null 2>&1 && return 0
  local tok req creds err
  tok=$(curl -fsS -m 3 -H "Metadata-Flavor: Google" \
    "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity?audience=$GCP_AUDIENCE&format=full" 2>/dev/null) \
    || die "no AWS credentials and not a GCP VM with a service account: attach the read role (AWS), or provide $LINKS_FILE"
  info "GCP VM detected: exchanging its Google identity for $GCP_AWS_ROLE_ARN"
  req=$(mktemp); chmod 600 "$req"
  python3 -c 'import json,sys; json.dump({"RoleArn":sys.argv[1],"RoleSessionName":sys.argv[2],"DurationSeconds":14400,"WebIdentityToken":sys.stdin.read().strip()}, open(sys.argv[3],"w"))' \
    "$GCP_AWS_ROLE_ARN" "jmdn-bootstrap-$(hostname -s | tr -c 'A-Za-z0-9=,.@_\n-' - | cut -c1-49)" "$req" <<<"$tok"
  unset tok
  creds=$(aws sts assume-role-with-web-identity --region "$BUCKET_REGION" --cli-input-json "file://$req" \
    --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text 2>"$req.err") \
    || { err=$(head -c 400 "$req.err"); rm -f "$req" "$req.err"; die "AWS refused the GCP identity: $err (check the role's trust policy has this VM's service account unique ID)"; }
  rm -f "$req" "$req.err"
  read -r AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN <<<"$creds"
  export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN
  unset creds
  aws sts get-caller-identity --region "$BUCKET_REGION" >/dev/null 2>&1 || die "assumed $GCP_AWS_ROLE_ARN but the credentials do not work"
}
# aws_done: drop the assumed-role credentials (GCP) once a download is finished,
# so later phases and the programs they run never inherit them.
aws_done() { unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN; }
fetch_url() { curl -fsSL --retry 3 -o "$2" "$1" || die "download failed for $(basename "$2") (link expired? make a new links file)"; }
SRC="/root/src"
REPO="/root/jmdn"
JMDN_DATA="/opt/jmdn"
JMDN_YAML="/etc/jmdn/jmdn.yaml"
IMPORT_DIR="/root/import"
MARK="/var/lib/jmdn-bootstrap"
PG_DB="thebedb"
PG_USER="thebedb"
CDC_SLOT="thebe_eventlog"
CDC_PUB="thebe_pub"
MIN_PG_MAJOR=18
SISTERS="avc ThebeDB JMDN-FastSync JMDN_Merkletree"

step() { printf '\n==> %s\n' "$*"; }
ok()   { printf '    OK  %s\n' "$*"; }
info() { printf '    ..  %s\n' "$*"; }
die()  { printf '\n    FAIL %s\n' "$*" >&2; exit 1; }
done_phase() { [ -f "$MARK/$1.done" ]; }
mark() { mkdir -p "$MARK"; touch "$MARK/$1.done"; }

[ "$(id -u)" -eq 0 ] || die "run as root"

# Inputs: taken from the environment if set, otherwise asked for (saved for re-runs).
INPUTS="$MARK/inputs"
mkdir -p "$MARK"; chmod 700 "$MARK"
# shellcheck disable=SC1090
if [ -f "$INPUTS" ]; then
  env_alias="$NODE_ALIAS" env_bls="$CANARY_BLS_PUB" env_reward="$REWARD_ADDRESS" env_local="$LOCAL_CONFIG"
  . "$INPUTS"
  [ -n "$env_alias" ] && NODE_ALIAS="$env_alias"
  [ -n "$env_bls" ] && CANARY_BLS_PUB="$env_bls"
  [ -n "$env_reward" ] && REWARD_ADDRESS="$env_reward"
  [ -n "$env_local" ] && LOCAL_CONFIG="$env_local"
  info "saved answers loaded from $INPUTS (values set in the environment take precedence)"
fi
CANARY_YAML="${LOCAL_CONFIG:-/root/jmdn-config-template.yaml}"
ask() {
  local var="$1" prompt="$2" re="$3" optional="${4:-}" val
  val="${!var:-}"
  while :; do
    if [ -z "$val" ]; then
      if [ ! -t 0 ]; then [ -n "$optional" ] && break; die "$var not set and no terminal to ask on"; fi
      read -r -p "    $prompt: " val
    fi
    if [ -z "$val" ] && [ -n "$optional" ]; then break; fi
    if [[ "$val" =~ $re ]]; then break; fi
    [ -t 0 ] || die "$var=\"$val\" is not valid and there is no terminal to ask again on"
    printf '    "%s" is not valid, try again\n' "$val"; val=""
  done
  printf -v "$var" '%s' "$val"
}
step "Inputs"
ask NODE_ALIAS "Node alias (e.g. jmdn-aws-us-west-1-v3base-2)" '^[A-Za-z0-9._-]+$'
ask CANARY_BLS_PUB "Canary BLS public key, HEX (see script header for the command)" '^[0-9a-fA-F]{64,}$'
ask REWARD_ADDRESS "Reward address 0x... (validators only; Enter to skip)" '^0x[0-9a-fA-F]{40}$' optional
if [ -n "$LOCAL_CONFIG" ]; then
  [ -f "$LOCAL_CONFIG" ] || die "LOCAL_CONFIG=$LOCAL_CONFIG not found"
fi
( umask 077; printf 'NODE_ALIAS=%q\nCANARY_BLS_PUB=%q\nREWARD_ADDRESS=%q\nLOCAL_CONFIG=%q\n' \
    "$NODE_ALIAS" "$CANARY_BLS_PUB" "$REWARD_ADDRESS" "$LOCAL_CONFIG" > "$INPUTS" )
ok "alias $NODE_ALIAS; reward address ${REWARD_ADDRESS:-none}; config ${LOCAL_CONFIG:-$CONFIG_S3}"
if [ -t 0 ]; then
  read -r -p "    Proceed? [y/N]: " go; [[ "$go" =~ ^[Yy]$ ]] || die "stopped at your request"
fi
export DEBIAN_FRONTEND=noninteractive
export PATH="/usr/local/go/bin:$PATH:/snap/bin"
export GOTOOLCHAIN=local

apt_wait() {
  local n=0
  while fuser /var/lib/dpkg/lock-frontend /var/lib/dpkg/lock /var/lib/apt/lists/lock >/dev/null 2>&1; do
    n=$((n+1)); [ "$n" -le 90 ] || die "apt lock held for 15 minutes; check: ps -ef | grep -E 'apt|dpkg'"
    [ "$n" -eq 1 ] && info "waiting for another apt/dpkg run to finish"
    sleep 10
  done
}

# Set "  key: value" inside a top-level "section:" of a yaml file, keeping any
# trailing comment. The value comes from the environment, never from argv.
yaml_set() {
  SECTION="$1" KEY="$2" python3 - "$JMDN_YAML" <<'PY'
import os, re, sys
path, sec, key, val = sys.argv[1], os.environ["SECTION"], os.environ["KEY"], os.environ["VAL"]
lines = open(path).read().split("\n")
in_sec, done = False, False
for i, l in enumerate(lines):
    if re.match(r"^[A-Za-z_][\w-]*:", l):
        in_sec = l.split(":")[0] == sec
        continue
    if in_sec:
        m = re.match(r"^(  " + re.escape(key) + r":)([^#\n]*)(\s*#.*)?$", l)
        if m:
            q = val if re.fullmatch(r"true|false|\d+", val) else '"' + val + '"'
            lines[i] = m.group(1) + " " + q + ((" " + m.group(3).strip()) if m.group(3) else "")
            done = True
            break
if not done:
    q = val if re.fullmatch(r"true|false|\d+", val) else '"' + val + '"'
    hdr = [i for i, l in enumerate(lines) if re.match(r"^" + re.escape(sec) + r":\s*(#.*)?$", l)]
    if not hdr:
        sys.exit(f"section {sec} not found in {path}")
    lines.insert(hdr[0] + 1, "  " + key + ": " + q)
open(path, "w").write("\n".join(lines))
PY
}

# ---- 1. system packages ---------------------------------------------------------
if ! done_phase 01-apt; then
  step "1/10 System packages"
  echo 'Acquire::ForceIPv4 "true";' > /etc/apt/apt.conf.d/99force-ipv4
  apt_wait; apt-get -o Acquire::http::Timeout=30 update -q
  apt_wait; apt-get -o Acquire::http::Timeout=30 install -y -q \
    git gcc make build-essential curl wget rsync python3 python3-yaml tar sqlite3 ca-certificates unzip psmisc openssl >/dev/null
  # AWS CLI: apt where the distro still ships it, else the snap (Ubuntu 24.04+
  # images without an awscli apt candidate, e.g. on GCP).
  if ! command -v aws >/dev/null 2>&1; then
    if apt-cache policy awscli 2>/dev/null | grep 'Candidate: [0-9]' >/dev/null; then
      apt_wait; apt-get install -y -q awscli >/dev/null
    else
      command -v snap >/dev/null 2>&1 || die "no awscli apt package and no snap; install the AWS CLI and rerun"
      snap install aws-cli --classic >/dev/null
    fi
  fi
  aws --version >/dev/null 2>&1 || die "AWS CLI not usable after install"
  mark 01-apt; ok "packages installed ($(aws --version 2>&1 | cut -d' ' -f1))"
fi

# ---- config template (after the AWS CLI is installed) ------------------------------
if ! done_phase 03-deps || ! done_phase 07-config; then
  if [ -z "$LOCAL_CONFIG" ]; then
    rm -f "$CANARY_YAML"
    if [ "$USE_LINKS" = 1 ]; then
      step "Config template (links file $LINKS_FILE)"
      ( umask 077; fetch_url "$CONFIG_URL" "$CANARY_YAML" )
    else
      step "Config template from $CONFIG_S3"
      aws_ready
      ( umask 077; aws s3 cp "$CONFIG_S3" "$CANARY_YAML" --region "$BUCKET_REGION" --only-show-errors ) \
        || die "could not fetch $CONFIG_S3 (needs s3:GetObject and kms:Decrypt on the config key)"
      aws_done
    fi
    chmod 600 "$CANARY_YAML"
    ok "downloaded to $CANARY_YAML (0600; removed after step 7)"
  fi
  python3 -c 'import yaml,sys; yaml.safe_load(open(sys.argv[1]))' "$CANARY_YAML" || die "$CANARY_YAML does not parse"
fi

# ---- 2. source: jmdn + private sister repos (HTTPS) -----------------------------
if ! done_phase 02-src; then
  step "2/10 Source code"
  [ -t 0 ] || die "the GitHub token is read from a terminal only; run this script interactively"
  printf '    GitHub token (hidden): '; read -rs GH_TOKEN; echo
  [ -n "$GH_TOKEN" ] || die "empty token"
  export GH_TOKEN
  for r in jmdn $SISTERS; do
    code=$(printf 'Authorization: Bearer %s\n' "$GH_TOKEN" | curl -s -o /dev/null -w '%{http_code}' -H @- "https://api.github.com/repos/JupiterMetaLabs/$r")
    [ "$code" = "200" ] || die "GitHub token check returned $code for $r (401 bad token, 404 no access to this repo, 403 needs org approval/SSO)"
  done
  ok "token can read jmdn and $SISTERS"
  export GIT_TERMINAL_PROMPT=0
  gitauth() { git -c credential.helper= -c credential.helper='!f() { echo username=x-access-token; echo "password=$GH_TOKEN"; }; f' "$@"; }
  if [ -d "$REPO/.git" ]; then gitauth -C "$REPO" fetch -q --tags origin; else gitauth clone -q https://github.com/JupiterMetaLabs/jmdn.git "$REPO"; fi
  mkdir -p "$SRC"
  for r in $SISTERS; do
    if [ -d "$SRC/$r/.git" ]; then gitauth -C "$SRC/$r" fetch -q --tags origin; else gitauth clone -q "https://github.com/JupiterMetaLabs/$r.git" "$SRC/$r"; fi
  done
  unset GH_TOKEN
  git -C "$REPO" rev-parse -q --verify "$JMDN_REF^{commit}" >/dev/null \
    || die "$JMDN_REF not found in jmdn (release not tagged yet?); set JMDN_REF to an existing tag or commit"
  git -C "$REPO" checkout -q "$JMDN_REF"
  for r in $SISTERS; do git config --global url."file://$SRC/$r".insteadOf "https://github.com/JupiterMetaLabs/$r"; done
  mark 02-src; ok "jmdn at $(git -C "$REPO" rev-parse --short HEAD); sister repos in $SRC"
fi

# ---- 3. Go, Yggdrasil, PostgreSQL, Redis ----------------------------------------
if ! done_phase 03-deps; then
  step "3/10 Dependencies (Go, Yggdrasil, PostgreSQL, Redis)"
  mkdir -p /etc/jmdn
  cp "$CANARY_YAML" "$JMDN_YAML"; chmod 600 "$JMDN_YAML"
  VAL="" python3 - "$JMDN_YAML" <<'PY'
import re, sys
p = sys.argv[1]; t = open(p).read()
t = re.sub(r'(?m)^(    password:)[^#\n]*', r'\1 ""', t, count=1)
open(p, "w").write(t)
PY
  mkdir -p "$MARK"; chmod 700 "$MARK"
  PG_APP_PASSWORD=$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'); export PG_APP_PASSWORD
  ( umask 077; printf '%s' "$PG_APP_PASSWORD" > "$MARK/pgpass" )
  # The snapshot is a Postgres 18 dump. Distros older than Ubuntu 26.04 ship an
  # older default; add the PostgreSQL apt repository so "postgresql" resolves to 18+.
  pg_candidate_major() { apt-cache policy postgresql 2>/dev/null | awk '/Candidate:/{print $2}' | sed 's/[^0-9].*//'; }
  pgc=$(pg_candidate_major)
  if [ -z "$(pg_lsclusters --no-header 2>/dev/null | awk -v m="$MIN_PG_MAJOR" '$1+0>=m')" ] && [ "${pgc:-0}" -lt "$MIN_PG_MAJOR" ]; then
    info "distro Postgres is ${pgc:-unavailable}; adding the PostgreSQL apt repository (apt.postgresql.org)"
    apt_wait; DEBIAN_FRONTEND=noninteractive apt-get install -y -q postgresql-common >/dev/null
    apt_wait; DEBIAN_FRONTEND=noninteractive /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh -y </dev/null >/dev/null \
      || die "could not add the PostgreSQL apt repository"
    pgc=$(pg_candidate_major); [ "${pgc:-0}" -ge "$MIN_PG_MAJOR" ] || die "postgresql candidate is still $(pg_candidate_major) after adding apt.postgresql.org"
  fi
  apt_wait
  # setup_dependencies prints the Thebe DSN with the password; redact it.
  ( cd "$REPO" && PG_APP_USER="$PG_USER" PG_APP_DB="$PG_DB" bash ./Scripts/setup_dependencies.sh --go --yggdrasil --redis --storage-local </dev/null ) 2>&1 \
    | sed -u -E 's#(postgres(ql)?://[^:/@[:space:]]+:)[^@[:space:]]+@#\1<redacted>@#g'
  command -v go >/dev/null || die "Go not on PATH after setup_dependencies"
  # setup_dependencies installs a fixed fallback Go; use the exact toolchain go.mod pins.
  want=$(awk '$1=="toolchain"{print $2; exit}' "$REPO/go.mod")
  [ -n "$want" ] || want="go$(awk '$1=="go"{print $2; exit}' "$REPO/go.mod")"
  have=$(GOTOOLCHAIN=local go version | awk '{print $3}')
  if [ "$have" != "$want" ]; then
    case "$(uname -m)" in x86_64) garch=amd64 ;; aarch64|arm64) garch=arm64 ;; *) die "unsupported arch $(uname -m)" ;; esac
    gfile="$want.linux-$garch.tar.gz"
    gsum=$(curl -fsSL "https://go.dev/dl/?mode=json&include=all" | python3 -c 'import sys,json; f=sys.argv[1]; print(next((x["sha256"] for r in json.load(sys.stdin) for x in r["files"] if x["filename"]==f), ""))' "$gfile")
    [ -n "$gsum" ] || die "no published checksum for $gfile"
    curl -fsSL -o "/tmp/$gfile" "https://go.dev/dl/$gfile"
    echo "$gsum  /tmp/$gfile" | sha256sum -c - >/dev/null || die "$gfile checksum mismatch"
    rm -rf /usr/local/go && tar -C /usr/local -xzf "/tmp/$gfile" && rm -f "/tmp/$gfile"
    info "Go $have replaced by $want (go.mod toolchain, checksum verified)"
  fi
  [ "$(GOTOOLCHAIN=local go version | awk '{print $3}')" = "$want" ] || die "Go is $(go version), want $want"
  go env -w GOPRIVATE=github.com/JupiterMetaLabs GONOSUMDB=github.com/JupiterMetaLabs
  mark 03-deps; ok "$(go version)"
fi

# ---- 4. Postgres for ThebeDB (role, replication, WAL) ----------------------------
if ! done_phase 04-pg; then
  step "4/10 PostgreSQL for ThebeDB"
  read -r PGVER PGPORT <<<"$(pg_lsclusters --no-header 2>/dev/null | awk -v m="$MIN_PG_MAJOR" '$4=="online" && $1+0>=m {print $1, $3}' | sort -n | tail -1)"
  [ -n "${PGPORT:-}" ] || die "no online Postgres $MIN_PG_MAJOR+ cluster (pg_lsclusters: $(pg_lsclusters --no-header 2>/dev/null | awk '{print $1"/"$2" "$4}' | tr '\n' ' '))"
  PG_APP_PASSWORD=$(cat "$MARK/pgpass")
  sudo -u postgres psql -p "$PGPORT" -v ON_ERROR_STOP=1 -q <<SQL
DO \$\$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$PG_USER') THEN
    CREATE ROLE $PG_USER LOGIN REPLICATION;
  END IF;
END \$\$;
ALTER ROLE $PG_USER LOGIN REPLICATION PASSWORD '$PG_APP_PASSWORD';
ALTER SYSTEM SET wal_level = 'logical';
ALTER SYSTEM SET max_slot_wal_keep_size = '2GB';
SQL
  sudo -u postgres psql -p "$PGPORT" -Atc "SELECT 1 FROM pg_database WHERE datname='$PG_DB'" | grep 1 >/dev/null \
    || sudo -u postgres createdb -p "$PGPORT" -O "$PG_USER" "$PG_DB"
  systemctl restart postgresql
  sleep 3
  [ "$(sudo -u postgres psql -p "$PGPORT" -Atc 'SHOW wal_level')" = "logical" ] || die "wal_level is not logical after restart"
  [ "$(sudo -u postgres psql -p "$PGPORT" -Atc "SELECT rolreplication FROM pg_roles WHERE rolname='$PG_USER'")" = "t" ] || die "$PG_USER lacks REPLICATION"
  echo "$PGPORT" > "$MARK/pgport"
  mark 04-pg; ok "Postgres $PGVER on port $PGPORT; role $PG_USER (LOGIN, REPLICATION); wal_level=logical"
fi
PGPORT=$(cat "$MARK/pgport")

# ---- 5. build -------------------------------------------------------------------
if ! done_phase 05-build; then
  step "5/10 Build jmdn"
  rm -f "$REPO/jmdn"
  ( cd "$REPO" && bash ./Scripts/build.sh )
  git -C "$REPO" checkout -q -- go.mod go.sum 2>/dev/null || true
  [ -x "$REPO/jmdn" ] || die "build.sh did not produce $REPO/jmdn"
  want_commit=$(git -C "$REPO" rev-parse --short HEAD)
  [ "$(git -C "$REPO" rev-parse HEAD)" = "$(git -C "$REPO" rev-parse "$JMDN_REF^{commit}")" ] || die "$REPO is not at $JMDN_REF"
  "$REPO/jmdn" --version 2>&1 | grep -F "$want_commit" >/dev/null || die "built jmdn is not $JMDN_REF ($want_commit): $("$REPO/jmdn" --version 2>&1 | head -1)"
  install -m 0755 "$REPO/jmdn" /usr/local/bin/jmdn
  mark 05-build; ok "built and installed: $(/usr/local/bin/jmdn --version 2>&1 | head -1)"
fi

# ---- 6. certs + service --------------------------------------------------------
if ! done_phase 06-service; then
  step "6/10 TLS certs and service"
  ( cd "$REPO" && bash ./Scripts/setup_certs.sh "$JMDN_DATA/certs" )
  ( cd "$REPO" && printf 'n\n' | bash ./Scripts/install_services.sh --mode local )
  systemctl stop jmdn 2>/dev/null || true
  [ -n "$(ls -A "$JMDN_DATA/certs" 2>/dev/null)" ] || die "no certs in $JMDN_DATA/certs"
  /usr/local/bin/jmdn --version 2>&1 | grep -F "$(git -C "$REPO" rev-parse --short HEAD)" >/dev/null || die "installed /usr/local/bin/jmdn is not $JMDN_REF after install_services"
  mark 06-service; ok "service installed and stopped; certs in $JMDN_DATA/certs"
fi

# ---- 7. node config -------------------------------------------------------------
if ! done_phase 07-config; then
  step "7/10 Node config ($JMDN_YAML)"
  [ "$(systemctl is-active jmdn 2>/dev/null || true)" != "active" ] || die "jmdn is running"
  PG_APP_PASSWORD=$(cat "$MARK/pgpass")
  VAL="$NODE_ALIAS" yaml_set node alias
  VAL="postgres://$PG_USER:$PG_APP_PASSWORD@127.0.0.1:$PGPORT/$PG_DB?sslmode=disable" yaml_set thebe sql_dsn
  VAL="$REWARD_ADDRESS" yaml_set consensus reward_address
  ADMIN_TOKEN=$(openssl rand -hex 32)
  ( umask 077; printf '%s\n' "$ADMIN_TOKEN" > /root/jmdn-admin-token )
  VAL="$ADMIN_TOKEN" yaml_set security admin_token
  unset ADMIN_TOKEN PG_APP_PASSWORD
  VAL="$CANARY_BLS_PUB" yaml_set checkpoint signer_bls_pub
  VAL="true" yaml_set checkpoint boot_fail_closed
  chmod 600 "$JMDN_YAML"
  python3 -c 'import yaml,sys; yaml.safe_load(open(sys.argv[1]))' "$JMDN_YAML" || die "$JMDN_YAML does not parse (above)"
  drift=$(diff <(grep -vE "alias:|sql_dsn:|password:|reward_address:|admin_token:|signer_bls_pub:|boot_fail_closed:" "$CANARY_YAML") \
               <(grep -vE "alias:|sql_dsn:|password:|reward_address:|admin_token:|signer_bls_pub:|boot_fail_closed:" "$JMDN_YAML") || true)
  [ -z "$drift" ] || { printf '%s\n' "$drift" | head -20; die "config differs from the template beyond the per-node keys (above)"; }
  mark 07-config; ok "alias, sql_dsn, reward_address, admin_token (/root/jmdn-admin-token), checkpoint pin set; rest identical to the template"
  [ -n "$LOCAL_CONFIG" ] || { shred -u "$CANARY_YAML" 2>/dev/null || rm -f "$CANARY_YAML"; }
fi

# ---- 8. snapshot ---------------------------------------------------------------
if ! done_phase 08-snapshot; then
  mkdir -p "$IMPORT_DIR"
  if [ "$USE_LINKS" = 1 ]; then
    step "8/10 Snapshot (links file $LINKS_FILE)"
    fetch_url "$PGDUMP_URL" "$IMPORT_DIR/thebe.pgdump"
    fetch_url "$TAR_URL" "$IMPORT_DIR/chaindata.tar"
    fetch_url "$SUMS_URL" "$IMPORT_DIR/SHA256SUMS"
    fetch_url "$HEIGHT_URL" "$IMPORT_DIR/COPY_HEIGHT"
  else
    step "8/10 Snapshot s3://$BUCKET/$PREFIX/"
    aws_ready
    aws s3 cp "s3://$BUCKET/$PREFIX/" "$IMPORT_DIR/" --recursive --region "$BUCKET_REGION" --only-show-errors
    aws_done
  fi
  ( cd "$IMPORT_DIR" && sha256sum -c SHA256SUMS >/dev/null ) || die "SHA256SUMS check failed"
  [ "$(sha256sum "$IMPORT_DIR/thebe.pgdump" | cut -c1-64)" = "$EXPECT_PGDUMP_SHA" ] || die "thebe.pgdump does not match the canary's checksum"
  [ "$(sha256sum "$IMPORT_DIR/chaindata.tar" | cut -c1-64)" = "$EXPECT_TAR_SHA" ] || die "chaindata.tar does not match the canary's checksum"
  [ "$(cat "$IMPORT_DIR/COPY_HEIGHT")" = "$EXPECT_HEIGHT" ] || die "COPY_HEIGHT is not $EXPECT_HEIGHT"
  mark 08-snapshot; ok "downloaded and verified against the canary (height $EXPECT_HEIGHT)"
fi

# ---- 9. restore -----------------------------------------------------------------
if ! done_phase 09-restore; then
  step "9/10 Restore chain data and Postgres"
  [ "$(systemctl is-active jmdn 2>/dev/null || true)" != "active" ] || die "jmdn is running"
  if [ -f "$MARK/09-restore.started" ]; then
    # An earlier run of this phase stopped partway. Everything below was put there
    # by that run, so clear it and restore again.
    info "previous restore did not finish: clearing $JMDN_DATA/storage and recreating database $PG_DB"
    find "$JMDN_DATA/storage" -mindepth 1 -delete 2>/dev/null || true
    sudo -u postgres psql -p "$PGPORT" -d "$PG_DB" -Atc "SELECT pg_drop_replication_slot('$CDC_SLOT') FROM pg_replication_slots WHERE slot_name='$CDC_SLOT'" >/dev/null 2>&1 || true
    sudo -u postgres dropdb -p "$PGPORT" --if-exists --force "$PG_DB"
    sudo -u postgres createdb -p "$PGPORT" -O "$PG_USER" "$PG_DB"
  fi
  [ -z "$(ls -A "$JMDN_DATA/storage" 2>/dev/null)" ] || die "$JMDN_DATA/storage is not empty and was not filled by this script; move it aside and rerun"
  grep -qs "$CANARY_PEER_ID" "$JMDN_DATA/config/peer.json" && die "$JMDN_DATA/config/peer.json holds the canary's identity"
  listing=$(tar -tf "$IMPORT_DIR/chaindata.tar") || die "cannot list chaindata.tar"
  if grep -qE '(^|/)config/(peer|bls)\.json$' <<<"$listing"; then
    die "chaindata.tar contains node identity files (config/peer.json or bls.json); refusing to extract"
  fi
  export PGHOST=127.0.0.1 PGPORT PGUSER="$PG_USER" PGDATABASE="$PG_DB" PGSSLMODE=disable
  PGPASSWORD=$(cat "$MARK/pgpass"); export PGPASSWORD
  trap 'unset PGPASSWORD' EXIT
  [ "$(psql -Atc "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'")" = "0" ] || die "database $PG_DB is not empty and was not filled by this script"
  touch "$MARK/09-restore.started"
  sudo -u postgres psql -p "$PGPORT" -d "$PG_DB" -Atc "SELECT pg_drop_replication_slot('$CDC_SLOT') FROM pg_replication_slots WHERE slot_name='$CDC_SLOT'" >/dev/null
  sudo -u postgres psql -p "$PGPORT" -d "$PG_DB" -Atc "DROP PUBLICATION IF EXISTS $CDC_PUB" >/dev/null
  tar -C "$JMDN_DATA" --exclude=config/peer.json --exclude=config/bls.json -xf "$IMPORT_DIR/chaindata.tar"
  rm -f "$JMDN_DATA/checkpoint-floor.json"
  info "pg_restore (a few minutes)"
  pg_restore --no-owner --no-privileges --no-publications --no-subscriptions -j 4 -d "$PG_DB" "$IMPORT_DIR/thebe.pgdump" || die "pg_restore reported errors (above)"
  maxblock=$(psql -Atc "SELECT max(block_number) FROM blocks")
  [ "$maxblock" = "$EXPECT_HEIGHT" ] || die "max(block_number) is $maxblock, expected $EXPECT_HEIGHT"
  # Freeze and analyze the restored tables now, before the CDC slot exists.
  # Otherwise autovacuum does it after boot and the slot holds that WAL
  # (about 1 GB) as lag until the first replicated commit.
  info "VACUUM (FREEZE, ANALYZE) of the restored tables (a few minutes)"
  sudo -u postgres psql -p "$PGPORT" -d "$PG_DB" -v ON_ERROR_STOP=1 -q -c "VACUUM (FREEZE, ANALYZE)" || die "VACUUM after restore failed"
  sudo -u postgres psql -p "$PGPORT" -v ON_ERROR_STOP=1 -q -c "CHECKPOINT" || die "CHECKPOINT failed"
  naccounts=$(psql -Atc "SELECT count(*) FROM accounts")
  unset PGPASSWORD
  sudo -u postgres psql -p "$PGPORT" -d "$PG_DB" -v ON_ERROR_STOP=1 -q -c "CREATE PUBLICATION $CDC_PUB FOR ALL TABLES"
  sudo -u postgres psql -p "$PGPORT" -d "$PG_DB" -v ON_ERROR_STOP=1 -Atc "SELECT pg_create_logical_replication_slot('$CDC_SLOT', 'pgoutput')" >/dev/null
  mark 09-restore; rm -f "$MARK/09-restore.started"; ok "accounts $naccounts, max block $maxblock; publication $CDC_PUB and slot $CDC_SLOT created"
fi

# ---- 10. start + first-boot checks ------------------------------------------------
step "10/10 Start and check"
since=$(date -u '+%Y-%m-%d %H:%M:%S')
systemctl reset-failed jmdn 2>/dev/null || true
if [ "$(systemctl is-active jmdn 2>/dev/null || true)" = "active" ]; then
  info "jmdn already running; restarting to re-check the boot"
  systemctl restart jmdn
else
  systemctl start jmdn
fi
result=""
for _ in $(seq 1 36); do
  sleep 5
  log=$(journalctl -u jmdn --since "$since UTC" --no-pager 2>/dev/null || true)
  if grep -qE "FATAL|boot verification FAILED|BLOCK APPLY HALTED" <<<"$log"; then result=fail; break; fi
  if grep -q "boot verification passed" <<<"$log"; then result=pass; break; fi
done
if [ "$result" = "fail" ]; then
  grep -E "FATAL|boot verification FAILED|BLOCK APPLY HALTED" <<<"$log" | tail -5
  die "jmdn did not boot (lines above)"
fi
[ "$result" = "pass" ] || die "no boot verification result after 3 minutes; check: journalctl -u jmdn --since '$since UTC'"
ok "boot verification passed (canary checkpoint, pinned key)"
grep -qs "$CANARY_PEER_ID" "$JMDN_DATA/config/peer.json" && die "running with the canary's peer ID"
peer=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("peer_id",""))' "$JMDN_DATA/config/peer.json" 2>/dev/null || true)
ok "own peer ID: ${peer:-see $JMDN_DATA/config/peer.json}"
sleep 30
tip=$(curl -s -m 5 -X POST -H 'content-type: application/json' --data '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}' http://localhost:8545 \
      | python3 -c 'import sys,json;print(int(json.load(sys.stdin)["result"],16))' 2>/dev/null || echo unavailable)
info "local tip: $tip (snapshot $EXPECT_HEIGHT)"
info "legacy-follow blocks applied: $(journalctl -u jmdn --since "$since UTC" --no-pager | grep -c "legacy follow: block applied and stored" || true)"

step "Done"
cat <<EOF
  Node is running. Next:
  - compare the tip and a few balances with the canary and mainnet-3
  - after this node has signed its own checkpoint (about one committee epoch) and
    $JMDN_DATA/config/bls.json exists, remove the canary pin:
      sed -i "s/^\([[:space:]]*signer_bls_pub:\).*/\1 ''/" $JMDN_YAML && systemctl restart jmdn
    then confirm "boot verification passed" again
  - admin token: /root/jmdn-admin-token (0600)
  - clean up when stable: rm -rf $IMPORT_DIR $MARK/pgpass
EOF
if [ "$USE_LINKS" = 1 ]; then echo "  - links file: shred -u $LINKS_FILE now that the node is up"; fi
