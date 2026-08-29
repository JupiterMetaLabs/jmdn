# Security Hardening Runbook (ops)

Operational, non-code hardening steps for a **production** `jmdn` node and its
ThebeDB store. These are the fixes that live in deployment, database roles,
network binds, and CI cadence — not in the Go source. Apply them in addition to
the in-code controls (SEC-03 boot posture validator, the fail-open consensus
flags, the anti-mutation SQL RULEs shipped in the migrations).

Scope note: paths, ports, and config keys below are the ones this node actually
uses. Verify against your live `jmdn.yaml` before applying — an operator may have
overridden them (every key also has a `JMDN_…` / `THEBE_…` env override).

---

## 1. Run Postgres as a least-privilege NON-OWNER role

**Why.** ThebeDB's canonical projection tables (`blocks`, `transactions`,
`snapshots`, `zk_proofs`, `l1_finality`, `contract_receipts`) are protected by
Postgres anti-mutation RULEs (`DO INSTEAD NOTHING` on UPDATE/DELETE — see §2).
Those RULEs are only a real control if the **application role cannot remove
them**. A role that **owns** a table can `ALTER TABLE … DISABLE RULE` or
`DROP RULE`, and can `UPDATE`/`DELETE` freely. If the app connects as the table
owner, the RULEs are decorative: a SQL-injection bug or a compromised app process
can turn them off and rewrite history.

**Fix — two roles:**

- `jmdn_owner` (migration/DDL role): owns the schema and tables, runs
  `golang-migrate` migrations. Used **only** by the migration step, never by the
  running node.
- `jmdn` (application role, the one in `thebe.sql_dsn`): **not** the table owner.
  Granted only `SELECT, INSERT` on the projection tables (plus `SELECT, INSERT,
  UPDATE, DELETE` on the few genuinely-mutable tables such as `accounts` and
  reconciliation state, if any). No `UPDATE`/`DELETE` on the append-only tables,
  no `ALTER`, no `DROP`, no ownership.

```sql
-- run as jmdn_owner (or a superuser doing setup), AFTER migrations are applied
REVOKE ALL   ON ALL TABLES IN SCHEMA public FROM jmdn;
GRANT  USAGE ON SCHEMA public TO jmdn;

-- append-only canonical tables: read + append only, no update/delete
GRANT SELECT, INSERT ON blocks, transactions, snapshots, zk_proofs,
                        l1_finality, contract_receipts TO jmdn;

-- mutable projection state (adjust to your actual mutable tables)
GRANT SELECT, INSERT, UPDATE, DELETE ON accounts TO jmdn;

-- sequences used by INSERTs
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO jmdn;

-- make future tables inherit the same default (owner runs this)
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT ON TABLES TO jmdn;
```

Point the node at the app role only:

```yaml
# jmdn.yaml
thebe:
  sql_dsn: "postgres://jmdn:<app-pw>@<host>:5430/jmdn?sslmode=require"
```

**Layering with the RULEs (belt + suspenders).** With the app role lacking
`UPDATE`/`DELETE`, an attempted mutation gets a hard `permission denied` — a loud,
auditable failure. If a grant is ever mistakenly widened, the RULEs still silently
swallow the UPDATE/DELETE (`DO INSTEAD NOTHING`) so history is preserved even
then. Keep both.

**Explicitly out of scope of this control (state it, don't hide it):** a Postgres
**superuser** always bypasses RULEs (e.g. `SET session_replication_role =
'replica'` disables all rules) and can drop them; the **table owner**
(`jmdn_owner`) can `DISABLE`/`DROP RULE`. Least-privilege only closes the
**application-role** path. Protect superuser/owner credentials separately (vault,
break-glass only, never in the node's env or `jmdn.yaml`), and never run the node
process with them.

**Verify:**

```sql
-- as the app role, these must FAIL or affect 0 rows:
UPDATE blocks SET block_number = block_number WHERE false;   -- permission denied
DELETE FROM transactions WHERE false;                        -- permission denied
-- confirm the app role owns nothing:
SELECT tablename FROM pg_tables WHERE schemaname='public' AND tableowner='jmdn';
-- expect: 0 rows
```

---

## 2. Apply the anti-mutation RULEs (and confirm they are present)

The RULEs ship in the migrations under
`DB_OPs/thebegateway/migrations/`:

- `000001_init_schema.up.sql` — `blocks`, `snapshots`, `transactions`,
  `zk_proofs`, `l1_finality`
- `000002_contract_receipt.up.sql` — `contract_receipts`

Each table gets two rules, e.g.:

```sql
CREATE OR REPLACE RULE rule_blocks_no_update AS ON UPDATE TO blocks DO INSTEAD NOTHING;
CREATE OR REPLACE RULE rule_blocks_no_delete AS ON DELETE TO blocks DO INSTEAD NOTHING;
```

**Apply** them by running the migrations as `jmdn_owner` (the DDL role), not the
app role:

```sh
migrate -path DB_OPs/thebegateway/migrations \
        -database "postgres://jmdn_owner:<pw>@<host>:5430/jmdn?sslmode=require" up
```

**Verify all 12 rules exist** (6 tables × {no_update, no_delete}):

```sql
SELECT tablename, rulename FROM pg_rules
WHERE schemaname='public' AND rulename LIKE 'rule_%_no_%'
ORDER BY tablename, rulename;
-- expect 12 rows across:
--   blocks, transactions, snapshots, zk_proofs, l1_finality, contract_receipts
```

If any are missing (e.g. a table was created outside migrations), re-run the
migration or add the matching `CREATE OR REPLACE RULE` pair before going live.

---

## 3. Bind admin / debug / eth surfaces to loopback or a trusted mesh

Several services default to `0.0.0.0` in `jmdn.yaml`. In production, only the
genuinely-public, rate-limited surfaces should face untrusted networks;
everything else must bind to loopback (`127.0.0.1`) or a trusted overlay
(Yggdrasil / WireGuard mesh), or sit behind the gatekeeper with auth.

Current binds (from `binds:` in `jmdn.yaml`) and the production target:

| Service (config key) | Port | Shipped bind | Production target |
|---|---|---|---|
| Explorer API (`api`) | 6090 | `0.0.0.0` | behind gatekeeper w/ auth, or loopback + reverse proxy |
| eth JSON-RPC facade (`facade`) | 6545 | `0.0.0.0` | public **only** if rate-limited (see §SEC-03); else loopback |
| eth WS (`ws`) | 6546 | `0.0.0.0` | loopback / trusted mesh unless intentionally public + rate-limited |
| eth gRPC (`geth`) | (see cfg) | loopback | keep loopback |
| Block ingest HTTP/admin (`blockgen`) | 16050 | `0.0.0.0` | **loopback / mesh only** (admin surface) |
| Block gRPC / P2P propagation (`blockgrpc`) | 16055 | `0.0.0.0` | public P2P (libp2p-authenticated) — OK |
| DID (`did`) | 16052 | `0.0.0.0` | loopback / mesh + auth |
| CLI (`cli`) | 16053 | `127.0.0.1` | keep loopback |
| thebe-debug (`thebe_debug`) | 19090 | `127.0.0.1` | keep loopback — **never** public |
| metrics (`metrics`) | 6050 | `127.0.0.1` | keep loopback; scrape via mesh/sidecar |
| profiler / pprof (`profiler`) | 6060 | `127.0.0.1` | keep loopback — **never** public |

**Rule of thumb:** `blockgrpc` (libp2p P2P) is the only surface that is public by
design; `blockgen` (admin ingest), `did`, `api`, `ws`, `facade` should be
loopback/mesh unless deliberately exposed with auth and rate limiting.
`thebe_debug`, `profiler`/pprof, `metrics`, and `cli` must **never** be reachable
off-box.

```yaml
# jmdn.yaml — production example
binds:
  api:      "127.0.0.1"    # front with an authenticated reverse proxy
  blockgen: "127.0.0.1"    # admin ingest — do NOT expose
  blockgrpc: "0.0.0.0"     # P2P (libp2p transport-authenticated)
  did:      "127.0.0.1"
  facade:   "127.0.0.1"    # or 0.0.0.0 ONLY with a rate limit configured
  ws:       "127.0.0.1"
  cli:      "127.0.0.1"
  thebe_debug: "127.0.0.1"
  metrics:  "127.0.0.1"
  profiler: "127.0.0.1"
```

The **SEC-03 boot posture validator** (`config/settings/security_posture.go`,
called from `main.go`) already warns on any auth-less gatekeeper service on a
non-loopback bind, and — with `security.strict_posture: true` — **refuses to
boot**. Set `strict_posture: true` in production so a mis-bind fails closed
instead of only logging a warning. Note the eth JSON-RPC facade is treated as
"public by design" and is exempted from the auth requirement but **must carry a
rate limit** (per-service `rate_limit` or `security.global_rate_limit`).

**Verify** after start:

```sh
ss -ltnp | grep -E ':(6090|6545|6546|16050|16052|19090|6060|6050)\b'
# admin/debug/metrics/pprof ports must show 127.0.0.1, not 0.0.0.0 or a public IP
```

---

## 4. Set Redis AUTH + TLS via the connection URL

The standalone cache/eventlog Redis is configured by `thebe.redis_url`
(`jmdn.yaml`) or `THEBE_REDIS_URL`. It ships as plaintext, no auth:

```yaml
thebe:
  redis_url: "redis://127.0.0.1:6379"
```

In production, require auth and TLS by using the `rediss://` scheme with
credentials in the URL:

```yaml
thebe:
  redis_url: "rediss://jmdn:<redis-pw>@<host>:6380/0"
```

Or via env (keeps the secret out of the file — preferred):

```sh
export THEBE_REDIS_URL='rediss://jmdn:<redis-pw>@<host>:6380/0'
```

On the Redis server: enable TLS (`tls-port 6380`, disable the plaintext `port 0`
if the mesh allows), set `requirepass` / an ACL user for `jmdn`, and bind Redis
itself to loopback or the trusted mesh. The node already **masks** the Redis URL
in logs (`maskRedisURL`), so credentials in the URL are not printed — but still
prefer the env var over committing the password to `jmdn.yaml`.

**Verify:**

```sh
redis-cli -u "$THEBE_REDIS_URL" PING          # PONG over TLS with auth
redis-cli -h <host> -p 6379 PING              # should be refused/unreachable in prod
```

---

## 5. Dependency & static-scan cadence (govulncheck / gosec / secrets)

The `security` job in `.github/workflows/ci.yml` runs on every push/PR to
`main` and `release/**`:

- `govulncheck ./...` — **hard gate** (fails the job on a reachable known vuln).
- `gosec` (medium severity/confidence, `-exclude-generated`) — **non-blocking**
  today (`continue-on-error: true`); triage the initial findings, then flip it to
  a hard gate.
- `gitleaks` secret scan — third-party action **pinned to a commit SHA**
  (`gitleaks/gitleaks-action@ff98106…`, v2.3.9). Org-owned repos must set the
  `GITLEAKS_LICENSE` secret for that action, or switch to the `gitleaks` binary
  (`gitleaks detect --source .`).

**Operational cadence beyond CI:**

- **Weekly scheduled run.** CI only fires on push/PR; a quiet repo can go weeks
  without a scan while new CVEs land against pinned deps. Add a `schedule:` (cron,
  e.g. Monday 06:00 UTC) trigger to the `security` job so `govulncheck` runs even
  with no commits.
- **On every dependency bump.** Run `govulncheck ./...` and `go mod verify`
  locally before merging any `go.mod`/`go.sum` change.
- **Triage window for gosec.** Review the non-blocking gosec output each sprint;
  once the backlog is clear, set `continue-on-error: false` to make it a gate.
- **Rotate on any gitleaks hit.** A secret-scan finding means *rotate the
  credential first*, then scrub history — the value is already compromised the
  moment it hit the remote.

Local one-liners (match the CI job; run with `CGO_ENABLED=1`, go-ethereum needs
cgo):

```sh
go install golang.org/x/vuln/cmd/govulncheck@latest
CGO_ENABLED=1 govulncheck ./...

go install github.com/securego/gosec/v2/cmd/gosec@latest
CGO_ENABLED=1 gosec -severity medium -confidence medium -exclude-generated ./...

gitleaks detect --source . --redact
```

---

## 6. Production consensus posture (fail-closed) — cross-reference

The three consensus hardening flags in `messaging/consensus_hardening.go`
(`RejectLegacyVotes`, `EnforceCommitteeRegistry`, `EnforceBodyBinding`) default
**ON** but can be disabled via env (`JMDN_REJECT_LEGACY_VOTES=0`, etc.). Disabling
any of them is **fail-open** — the node then accepts legacy/unbound/non-committee
consensus input.

`main.go` now calls `messaging.ValidateProductionConsensusPosture()` at startup:
in a **production posture** (`security.strict_posture: true` **or**
`network.environment: mainnet`) the node **refuses to boot** if any of the three
is disabled. Operationally: never set those `JMDN_*` flags to `0` on a mainnet /
strict-posture node except during a coordinated, whole-network mixed-version
rollout — and clear them again the moment the rollout completes.

**Verify** (should refuse to start):

```sh
JMDN_REJECT_LEGACY_VOTES=0 ./jmdn   # with strict_posture:true or environment:mainnet
# expect: "Refusing to start: SEC-03 production consensus posture: … RejectLegacyVotes …"
```
