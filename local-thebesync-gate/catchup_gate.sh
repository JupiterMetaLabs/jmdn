#!/usr/bin/env bash
# ThebeSync (FastSync v4) catch-up gate.
#
# Proves that a lagging/fresh node (B) brought up to speed via `catchup` (which now
# routes through thebesync.CatchUp) ends byte-identical to the source node (A):
# same tip height, same block hashes, same sampled account balances.
#
# Exercises the full path end-to-end: A serves via /fastsync/v4/{head,getblocks};
# B fetches, verifies (body-binding + committee cert + linkage), applies through
# ProcessBlockTransactions (P2.5 fingerprint halt), advances its tip.
#
# Prereqs: two running jmdn nodes exposing the Ethereum-compatible JSON-RPC facade.
#   A = source, ahead (sequencer or a synced node).
#   B = target, behind A (fresh node past genesis, or lagging).
#
# Usage:
#   NODE_A_RPC=http://localhost:8545 \
#   NODE_B_RPC=http://localhost:8546 \
#   A_MULTIADDR=/ip4/127.0.0.1/tcp/15000/p2p/12D3KooW... \
#   B_CATCHUP="docker exec jmdn-nodeB /app/jmdn -cmd catchup" \
#   SAMPLE_ACCOUNTS="0xabc...,0xdef..." \
#   ./catchup_gate.sh
#
# B_CATCHUP is the command that triggers catch-up ON NODE B; the script appends
# A_MULTIADDR as the final argument.
#
# Exit 0 = PASS (B == A). Non-zero = FAIL, printing the first divergence.
set -euo pipefail

NODE_A_RPC="${NODE_A_RPC:?set NODE_A_RPC (A eth JSON-RPC URL)}"
NODE_B_RPC="${NODE_B_RPC:?set NODE_B_RPC (B eth JSON-RPC URL)}"
A_MULTIADDR="${A_MULTIADDR:?set A_MULTIADDR (A libp2p multiaddr with /p2p/<id>)}"
B_CATCHUP="${B_CATCHUP:?set B_CATCHUP (command that runs catchup on node B)}"
SAMPLE_ACCOUNTS="${SAMPLE_ACCOUNTS:-}"   # optional comma-separated 0x addresses
POLL_TIMEOUT="${POLL_TIMEOUT:-120}"      # seconds to wait for B to reach A tip
HASH_SAMPLES="${HASH_SAMPLES:-8}"        # heights to spot-check block hashes

# rpc <url> <method> <params-json> <field>
# field: JSON key inside "result" (e.g. hash), or empty for the whole result.
# Prints the value; exits non-zero on RPC/JSON error.
rpc() {
  local url="$1" method="$2" params="$3" field="${4:-}"
  curl -sf -X POST -H 'Content-Type: application/json' \
    --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"${method}\",\"params\":${params}}" \
    "$url" | FIELD="$field" python3 -c '
import sys, json, os
d = json.load(sys.stdin)
if "error" in d and d["error"]:
    sys.stderr.write("rpc error: %s\n" % json.dumps(d["error"])); sys.exit(3)
r = d.get("result")
f = os.environ.get("FIELD","")
if f:
    if not isinstance(r, dict) or f not in r:
        sys.stderr.write("missing field %r in result\n" % f); sys.exit(4)
    print(r[f])
else:
    print(r)
'
}

hex2dec() { python3 -c 'import sys;print(int(sys.argv[1],16))' "$1"; }

tip() { hex2dec "$(rpc "$1" eth_blockNumber '[]')"; }
block_hash() { # <url> <decimal-height>
  local hx; hx="$(python3 -c 'print(hex(int(__import__("sys").argv[1])))' "$2")"
  rpc "$1" eth_getBlockByNumber "[\"$hx\",false]" hash
}
balance() { rpc "$1" eth_getBalance "[\"$2\",\"latest\"]"; }  # whole result (0xhex)

echo "== ThebeSync catch-up gate =="
A_TIP="$(tip "$NODE_A_RPC")"; B_TIP0="$(tip "$NODE_B_RPC")"
echo "A tip=$A_TIP  B tip(before)=$B_TIP0"
if (( B_TIP0 >= A_TIP )); then
  echo "SKIP: B already at/ahead of A ($B_TIP0 >= $A_TIP). Reset B lower first."; exit 2
fi

echo "-- triggering catch-up on B → $A_MULTIADDR"
$B_CATCHUP "$A_MULTIADDR" || { echo "FAIL: catchup command errored"; exit 1; }

echo "-- waiting for B to reach tip $A_TIP (timeout ${POLL_TIMEOUT}s)"
deadline=$(( $(date +%s) + POLL_TIMEOUT ))
while :; do
  B_TIP="$(tip "$NODE_B_RPC")"
  (( B_TIP >= A_TIP )) && break
  (( $(date +%s) > deadline )) && { echo "FAIL: B stuck at $B_TIP < A $A_TIP (timeout)"; exit 1; }
  sleep 2
done
echo "B reached tip=$B_TIP"

echo "-- comparing block hashes at up to $HASH_SAMPLES heights (+ tip)"
step=$(( A_TIP / HASH_SAMPLES )); (( step < 1 )) && step=1
for (( h=0; h<=A_TIP; h+=step )); do
  ha="$(block_hash "$NODE_A_RPC" "$h")"; hb="$(block_hash "$NODE_B_RPC" "$h")"
  [[ "$ha" == "$hb" ]] || { echo "FAIL: block $h hash mismatch  A=$ha  B=$hb"; exit 1; }
done
ha="$(block_hash "$NODE_A_RPC" "$A_TIP")"; hb="$(block_hash "$NODE_B_RPC" "$A_TIP")"
[[ "$ha" == "$hb" ]] || { echo "FAIL: tip $A_TIP hash mismatch A=$ha B=$hb"; exit 1; }
echo "block hashes match through tip"

if [[ -n "$SAMPLE_ACCOUNTS" ]]; then
  echo "-- comparing sample account balances"
  IFS=',' read -ra ACCTS <<< "$SAMPLE_ACCOUNTS"
  for a in "${ACCTS[@]}"; do
    ba="$(balance "$NODE_A_RPC" "$a")"; bb="$(balance "$NODE_B_RPC" "$a")"
    [[ "$ba" == "$bb" ]] || { echo "FAIL: balance mismatch $a  A=$ba  B=$bb"; exit 1; }
    echo "  $a  $ba  ok"
  done
fi

echo "== GATE PASS: node B is byte-identical to node A through tip $A_TIP =="
