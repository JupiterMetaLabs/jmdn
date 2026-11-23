#!/usr/bin/env bash
# jmdn_v3base_node_bootstrap.sh — one run from a fresh Ubuntu VM to a running
# jmdn v3base node seeded from the canary snapshot in S3.
#
# Before running (once per node):
#   1. Attach an IAM role with s3:ListBucket + s3:GetObject on the snapshot bucket
#      (AWS). The script checks it.
#   2. Copy the canary's /etc/jmdn/jmdn.yaml to this VM as /root/canary-jmdn.yaml.
#   3. Get the canary's BLS public key as HEX (bls.json stores it as base64). On the canary:
#        python3 -c 'import json,base64;print(base64.b64decode(json.load(open("/opt/jmdn/config/bls.json"))["bls_pub"]).hex())'
#   4. Have a GitHub fine-grained token (Contents: read) for jmdn, avc, ThebeDB,
#      JMDN-FastSync, JMDN_Merkletree. The script asks for it, hidden; it is never
#      written to disk or put on a command line.
#
# Run:
#   sudo bash jmdn_v3base_node_bootstrap.sh
# It asks for the node alias, the canary's BLS key (hex), an optional reward
# address and the canary yaml path, then the GitHub token. Answers are saved
# (0600) for re-runs; NODE_ALIAS etc. can also be set in the environment.
#
# Re-runnable: each finished phase leaves a marker in /var/lib/jmdn-bootstrap and is
# skipped next time. Any failed check stops the run with the reason.

set -euo pipefail

# ---- release and snapshot -------------------------------------------------------
JMDN_REF="${JMDN_REF:-9031eeb5}"
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
CANARY_YAML="${CANARY_YAML:-/root/canary-jmdn.yaml}"
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
[ -f "$INPUTS" ] && . "$INPUTS"
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
    printf '    "%s" is not valid, try again\n' "$val"; val=""
  done
  printf -v "$var" '%s' "$val"
}
step "Inputs"
ask NODE_ALIAS "Node alias (e.g. jmdn-aws-us-west-1-v3base-2)" '^[A-Za-z0-9._-]+$'
ask CANARY_BLS_PUB "Canary BLS public key, HEX (see script header for the command)" '^[0-9a-fA-F]{64,}$'
ask REWARD_ADDRESS "Reward address 0x... (validators only; Enter to skip)" '^0x[0-9a-fA-F]{40}$' optional
ask CANARY_YAML "Path to the canary's jmdn.yaml copy" '^/.+'
[ -f "$CANARY_YAML" ] || die "$CANARY_YAML not found (copy the canary's /etc/jmdn/jmdn.yaml here)"
( umask 077; printf 'NODE_ALIAS=%q\nCANARY_BLS_PUB=%q\nREWARD_ADDRESS=%q\nCANARY_YAML=%q\n' \
    "$NODE_ALIAS" "$CANARY_BLS_PUB" "$REWARD_ADDRESS" "$CANARY_YAML" > "$INPUTS" )
ok "alias $NODE_ALIAS; reward address ${REWARD_ADDRESS:-none}; canary yaml $CANARY_YAML"
if [ -t 0 ]; then
  read -r -p "    Proceed? [y/N]: " go; [[ "$go" =~ ^[Yy]$ ]] || die "stopped at your request"
fi
export DEBIAN_FRONTEND=noninteractive
export PATH="/usr/local/go/bin:$PATH"

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
    git gcc make build-essential curl wget rsync python3 python3-yaml tar sqlite3 ca-certificates unzip awscli psmisc openssl >/dev/null
  mark 01-apt; ok "packages installed"
fi

# ---- 2. source: jmdn + private sister repos (HTTPS) -----------------------------
if ! done_phase 02-src; then
  step "2/10 Source code"
  printf '    GitHub token (hidden): '; read -rs GH_TOKEN; echo
  [ -n "$GH_TOKEN" ] || die "empty token"
  export GH_TOKEN
  code=$(printf 'Authorization: Bearer %s\n' "$GH_TOKEN" | curl -s -o /dev/null -w '%{http_code}' -H @- https://api.github.com/repos/JupiterMetaLabs/avc)
  [ "$code" = "200" ] || die "GitHub token check returned $code for avc (401 bad token, 404 no access, 403 needs org approval/SSO)"
  gclone() { git -c credential.helper= -c credential.helper='!f() { echo username=x-access-token; echo "password=$GH_TOKEN"; }; f' clone -q "https://github.com/JupiterMetaLabs/$1.git" "$2"; }
  [ -d "$REPO/.git" ] || gclone jmdn "$REPO"
  mkdir -p "$SRC"
  for r in $SISTERS; do [ -d "$SRC/$r/.git" ] || gclone "$r" "$SRC/$r"; done
  unset GH_TOKEN
  git -C "$REPO" fetch -q origin || true
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
  apt_wait
  ( cd "$REPO" && PG_APP_USER="$PG_USER" PG_APP_DB="$PG_DB" ./Scripts/setup_dependencies.sh --all --storage-local </dev/null )
  command -v go >/dev/null || die "Go not on PATH after setup_dependencies"
  go env -w GOPRIVATE=github.com/JupiterMetaLabs GONOSUMDB=github.com/JupiterMetaLabs
  mark 03-deps; ok "$(go version)"
fi

# ---- 4. Postgres for ThebeDB (role, replication, WAL) ----------------------------
if ! done_phase 04-pg; then
  step "4/10 PostgreSQL for ThebeDB"
  PGPORT=$(pg_lsclusters --no-header 2>/dev/null | awk '$4=="online"{print $3; exit}')
  [ -n "$PGPORT" ] || die "no online Postgres cluster (pg_lsclusters)"
  PGVER=$(pg_lsclusters --no-header | awk '$4=="online"{print $1; exit}')
  [ "${PGVER%%.*}" -ge "$MIN_PG_MAJOR" ] || die "Postgres $PGVER < $MIN_PG_MAJOR; the snapshot is a Postgres 18 dump"
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
  sudo -u postgres psql -p "$PGPORT" -Atc "SELECT 1 FROM pg_database WHERE datname='$PG_DB'" | grep -q 1 \
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
  ( cd "$REPO" && ./Scripts/build.sh )
  git -C "$REPO" checkout -q -- go.mod go.sum 2>/dev/null || true
  /usr/local/bin/jmdn --version 2>&1 | grep -q "${JMDN_REF:0:7}" || die "installed jmdn is not $JMDN_REF: $(/usr/local/bin/jmdn --version 2>&1 | head -1)"
  mark 05-build; ok "$(/usr/local/bin/jmdn --version 2>&1 | head -1)"
fi

# ---- 6. certs + service --------------------------------------------------------
if ! done_phase 06-service; then
  step "6/10 TLS certs and service"
  ( cd "$REPO" && ./Scripts/setup_certs.sh "$JMDN_DATA/certs" )
  ( cd "$REPO" && printf 'n\n' | ./Scripts/install_services.sh --mode local )
  systemctl stop jmdn 2>/dev/null || true
  [ -n "$(ls -A "$JMDN_DATA/certs" 2>/dev/null)" ] || die "no certs in $JMDN_DATA/certs"
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
  [ -z "$drift" ] || { printf '%s\n' "$drift" | head -20; die "config differs from the canary beyond the per-node keys (above)"; }
  mark 07-config; ok "alias, sql_dsn, reward_address, admin_token (/root/jmdn-admin-token), checkpoint pin set; rest identical to the canary"
fi

# ---- 8. snapshot ---------------------------------------------------------------
if ! done_phase 08-snapshot; then
  step "8/10 Snapshot s3://$BUCKET/$PREFIX/"
  aws sts get-caller-identity >/dev/null 2>&1 || die "no AWS credentials: attach the read role to this VM"
  mkdir -p "$IMPORT_DIR"
  aws s3 cp "s3://$BUCKET/$PREFIX/" "$IMPORT_DIR/" --recursive --region "$BUCKET_REGION" --only-show-errors
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
  [ -z "$(ls -A "$JMDN_DATA/storage" 2>/dev/null)" ] || die "$JMDN_DATA/storage is not empty"
  grep -qs "$CANARY_PEER_ID" "$JMDN_DATA/config/peer.json" && die "$JMDN_DATA/config/peer.json holds the canary's identity"
  export PGHOST=127.0.0.1 PGPORT PGUSER="$PG_USER" PGDATABASE="$PG_DB" PGSSLMODE=disable
  PGPASSWORD=$(cat "$MARK/pgpass"); export PGPASSWORD
  trap 'unset PGPASSWORD' EXIT
  [ "$(psql -Atc "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'")" = "0" ] || die "database $PG_DB is not empty"
  sudo -u postgres psql -p "$PGPORT" -d "$PG_DB" -Atc "SELECT pg_drop_replication_slot('$CDC_SLOT') FROM pg_replication_slots WHERE slot_name='$CDC_SLOT'" >/dev/null
  sudo -u postgres psql -p "$PGPORT" -d "$PG_DB" -Atc "DROP PUBLICATION IF EXISTS $CDC_PUB" >/dev/null
  tar -C "$JMDN_DATA" -xf "$IMPORT_DIR/chaindata.tar"
  rm -f "$JMDN_DATA/checkpoint-floor.json"
  info "pg_restore (a few minutes)"
  pg_restore --no-owner --no-privileges --no-publications --no-subscriptions -j 4 -d "$PG_DB" "$IMPORT_DIR/thebe.pgdump" || die "pg_restore reported errors (above)"
  maxblock=$(psql -Atc "SELECT max(block_number) FROM blocks")
  [ "$maxblock" = "$EXPECT_HEIGHT" ] || die "max(block_number) is $maxblock, expected $EXPECT_HEIGHT"
  naccounts=$(psql -Atc "SELECT count(*) FROM accounts")
  unset PGPASSWORD
  sudo -u postgres psql -p "$PGPORT" -d "$PG_DB" -v ON_ERROR_STOP=1 -q -c "CREATE PUBLICATION $CDC_PUB FOR ALL TABLES"
  sudo -u postgres psql -p "$PGPORT" -d "$PG_DB" -v ON_ERROR_STOP=1 -Atc "SELECT pg_create_logical_replication_slot('$CDC_SLOT', 'pgoutput')" >/dev/null
  mark 09-restore; ok "accounts $naccounts, max block $maxblock; publication $CDC_PUB and slot $CDC_SLOT created"
fi

# ---- 10. start + first-boot checks ------------------------------------------------
step "10/10 Start and check"
since=$(date -u '+%Y-%m-%d %H:%M:%S')
systemctl reset-failed jmdn 2>/dev/null || true
systemctl start jmdn
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