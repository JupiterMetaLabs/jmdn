#!/usr/bin/env bash
# check_slot_alignment.sh — read-only check that a node's committed chain
# follows the slot rule (slot = block number) before upgrading to a build that
# enforces it on live receive and on sync.
#
# Usage (pick one):
#   Scripts/check_slot_alignment.sh --docker <postgres-container>
#   Scripts/check_slot_alignment.sh --dsn "postgres://jmdn:<pw>@127.0.0.1:5430/jmdn?sslmode=require"
#
# Runs SELECTs only. Prints the counts, up to 20 offending blocks, and a final
# PASS / FAIL line. Run it on the sequencer and at least two validators, over
# the whole chain.
#
# PASS means every block has slot = block number, except a leading run of
# slot-0 blocks from before slots were stamped (accepted on sync only while the
# parent is also slot 0).

set -euo pipefail

mode="${1:-}"; target="${2:-}"
if [[ -z "$mode" || -z "$target" ]]; then
  sed -n '6,8p' "$0"; exit 2
fi

run_sql() {
  case "$mode" in
    --docker) docker exec -i "$target" psql -U jmdn -d jmdn -v ON_ERROR_STOP=1 -At -F '|' -c "$1" ;;
    --dsn)    psql "$target" -v ON_ERROR_STOP=1 -At -F '|' -c "$1" ;;
    *) echo "unknown mode: $mode" >&2; exit 2 ;;
  esac
}

summary=$(run_sql "
WITH b AS (
  SELECT block_number AS h,
         COALESCE((extra_data->>'slot')::numeric, 0) AS slot,
         COALESCE((extra_data->>'period')::numeric, 0) AS period,
         LAG(COALESCE((extra_data->>'slot')::numeric, 0)) OVER (ORDER BY block_number) AS parent_slot
  FROM blocks)
SELECT COUNT(*),
       COALESCE(MAX(h), 0),
       COUNT(*) FILTER (WHERE slot = h),
       COUNT(*) FILTER (WHERE slot = 0 AND h > 0 AND COALESCE(parent_slot, 0) = 0),
       COUNT(*) FILTER (WHERE slot <> h AND NOT (slot = 0 AND COALESCE(parent_slot, 0) = 0)),
       COUNT(*) FILTER (WHERE period > 0)
FROM b;")

IFS='|' read -r total tip aligned preslot bad periods <<<"$summary"
gaps=$(run_sql "SELECT COUNT(*) FROM (SELECT block_number - LAG(block_number) OVER (ORDER BY block_number) AS d FROM blocks) x WHERE d > 1;")

echo "blocks:              $total (tip $tip)"
echo "slot = block number: $aligned"
echo "pre-slot (slot 0):   $preslot"
echo "violations:          $bad"
echo "period > 0:          $periods"
echo "height gaps:         $gaps"

if [[ "$bad" != "0" ]]; then
  echo "--- first violations (block | slot | parent slot | period):"
  run_sql "
WITH b AS (
  SELECT block_number AS h,
         COALESCE((extra_data->>'slot')::numeric, 0) AS slot,
         COALESCE((extra_data->>'period')::numeric, 0) AS period,
         LAG(COALESCE((extra_data->>'slot')::numeric, 0)) OVER (ORDER BY block_number) AS parent_slot
  FROM blocks)
SELECT h, slot, COALESCE(parent_slot, 0), period FROM b
WHERE slot <> h AND NOT (slot = 0 AND COALESCE(parent_slot, 0) = 0)
ORDER BY h LIMIT 20;"
fi

if [[ "$bad" == "0" && "$gaps" == "0" ]]; then
  echo "RESULT: PASS"
else
  echo "RESULT: FAIL — do not deploy the slot = block number build to this chain until this is understood"
  exit 1
fi
