# Testnet fleet upgrade → v3base `12cde77b` + node repairs

Prepared 2026-10-08 from the fleet state at tip 971 (23/31 in sync; node-1/node-3 on
server-1 behind at 963; 6 nodes not answering; testnet-aws-node-4 halted at 911).

## 0. What `12cde77b` changes vs the running `cec52d4` (verified from the diff)

| PR | Effect | Why it matters to us |
|---|---|---|
| #187 `aa4d161` | votes authorized against the **uncapped pool** under `JMDN_COMMITTEE_V2` | fixes the selection ⊆ authorization defect we found at block 953 (v2 committee members denied `not_in_eligible_set`, alphabetical-7 votes counted) |
| #188 `1e7151f` | every validator's vote reaches the buddy tally; per-vote BLS stamped on wire; ingest on direct + pubsub paths; new metrics `validator_vote_ingest_total`, per-block tally log | flags `JMDN_VALIDATOR_VOTE_INGEST`, `JMDN_VALIDATOR_VOTER_SET` — default **on**; leave unset |
| #189 `12cde77` | VDF proof pull rate-limit; equivocation dedup re-keyed to ConsensusHash; **missing ConsensusHash now rejected unconditionally** | consensus rule tightened → upgrade the whole fleet together, not rolling over days |
| #183 `d8ff4b3` | ART identity stamped for EVM-created accounts (block 936 withhold) | new key `consensus.evm_unstamped_account_revert_height`, default 0 = legacy; keep 0 |
| #182 `829d6d9` | txindex catch-up indexes transactions; one-time re-index from genesis at boot (`catchup_version=2`) | first boot after upgrade does extra work; explorer tx counts fall back to COUNT(*) until ready |
| #184 | no false ERROR on empty tally | log noise only |

Not changed: wire protocol IDs / pubsub topics, DB migrations, Go toolchain (1.26),
`go.mod`. **Not fixed by this commit:** the `(858,0)` `uq_txn_block_index` retry loop
that filled the disk on 2026-10-06, Docker/Postgres log rotation, the inactive
`thebe_eventlog` replication slot. Those are handled in §2 below.

## 1. Timing

Tip 971, epoch boundary at 1000. Do the fleet restart **now, finishing before ~990**,
or wait until **after 1002** (epoch 20 seated). Never restart nodes in 995–1005.

Before touching anything, settle epoch 19 (one command on testnet-seq):

```bash
journalctl -u jmdn --no-pager | grep -E "entered fallback|finalised via the|fallback deadline exceeded|VDF sealing started" | cut -c1-240
for c in jmdt-node-2 jmdt-node-3; do docker logs --since 72h $c 2>&1 | grep -E "finalised via the|fallback deadline exceeded|VDF sealing started" | grep -E '"epoch":19|for_epoch":20' | cut -c1-200 | tail -3; done
```

`finalised via the …` (19) + `VDF sealing started … "for_epoch":20` → proceed.
`fallback deadline exceeded` (19) → the chain halts at 1000 regardless of binary; stop
and decide §8.1 rollback with the colleague before upgrading.

## 2. Repairs that are independent of the binary

### 2.1 Stop the `(858,0)` retry loop before it refills the disk (testnet-seq)

```bash
a=$(wc -l < /var/log/postgresql/postgresql-16-main.log); sleep 10; b=$(wc -l < /var/log/postgresql/postgresql-16-main.log); echo "$((b-a)) lines/10s"
```

If > 0: the retry lives in jmdn memory (it is not going through the bounded SQLite
outbox — `thebe_outbox` has `MaxOutboxAttempts=12`). The sequencer restart in §4 clears
it; the code fix (bounded retry on permanent constraint errors / `ON CONFLICT
(block_number, tx_index)`) is filed for the colleague. Until the restart, keep the log
in check: `truncate -s 0` it again if it passes 1 GB.

### 2.2 Log rotation so this cannot recur (testnet-seq + every server)

```bash
# Docker: cap container json logs (takes effect for containers created after restart → do it during the §4 window)
cat > /etc/docker/daemon.json <<'EOF'
{ "log-driver": "json-file", "log-opts": { "max-size": "200m", "max-file": "3" } }
EOF
# Postgres: size-triggered rotation (daily rotation let one day reach 2.4 GB)
cat > /etc/logrotate.d/postgresql-size <<'EOF'
/var/log/postgresql/*.log { size 500M rotate 3 compress delaycompress missingok notifempty copytruncate }
EOF
# journald: persistent, bounded
sed -i 's/^#\?Storage=.*/Storage=persistent/; s/^#\?SystemMaxUse=.*/SystemMaxUse=2G/' /etc/systemd/journald.conf && systemctl restart systemd-journald
```

### 2.3 Replication slot (testnet-seq)

```bash
sudo -u postgres psql -p 5430 -d jmdn -c "SELECT slot_name, active, pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)) AS retained_wal FROM pg_replication_slots;"
sudo -u postgres psql -p 5430 -d jmdn -c "ALTER SYSTEM SET max_slot_wal_keep_size = '8GB'; SELECT pg_reload_conf();"
```

`active = f` after the sequencer restart in §4 means the eventlog consumer is wedged →
hand to colleague; do **not** drop the slot without them.

### 2.4 `jmdt-postgres-node-1` restart loop (server-1)

```bash
docker logs --tail 40 jmdt-postgres-node-1 2>&1 | cut -c1-200
docker inspect -f '{{range .Mounts}}{{.Source}} {{end}}' jmdt-postgres-node-1 | xargs df -h
```

Most likely: it was mid-write when the disk hit 100% and now fails recovery, or
`postmaster.pid` is stale. `docker restart jmdt-postgres-node-1` once the root disk has
headroom usually clears it; if the log says a WAL segment is corrupt, treat node-1 like
node-4 below (wipe + resync).

### 2.5 testnet-aws-node-4 — state divergence at 912 (AWS host)

```bash
cd <compose dir>; docker compose stop jmdt-node-4
# back up, then clear state; KEEP the libp2p identity key so the peer id stays 12D3KooWCw9m…
tar czf /home/ubuntu/node4-state-$(date +%F).tgz /opt/jmdn/storage 2>/dev/null
rm -rf /opt/jmdn/storage/thebe-kv/*
docker exec -i jmdt-postgres-node-4 sh -c 'psql -U "$POSTGRES_USER" -d postgres -c "DROP DATABASE jmdn;" -c "CREATE DATABASE jmdn OWNER \"$POSTGRES_USER\";"'
# remove the stale one-off repair from its env (compose/.env): JMDN_REPROJECT_RANGE=821-835
```

Start it with the **new** image in §4 (not before — the vote-authorization fix is what
lets a resyncing node pass `VerifySyncedBlockCertificate` against the v2 committee).

### 2.6 The 6 "no answer" nodes

Dashboard pattern suggests one whole server (5 dark) + 1. On that host:

```bash
docker ps -a --format 'table {{.Names}}\t{{.Status}}'; df -h /; wg show | head -20
for c in $(docker ps -a --format '{{.Names}}' | grep -E '^jmdt-node-[0-9]+$'); do echo "== $c"; docker logs --tail 5 $c 2>&1 | cut -c1-160; done
```

Expect either disk-full (same as testnet-seq — apply §2.2 and free space) or WireGuard
down (`wg show` with no recent handshake). Bring them back on the new image in §4.

## 3. Build once, on testnet-seq

```bash
cd ~/jmdn && git fetch origin && git checkout 12cde77b7727bcbfdb16bbeb3ed477737ceb99d6
git log --oneline -1          # must print 12cde77 Fix/consensus and vdf hardening v2 (#189)
go version                    # 1.26.x
CGO_ENABLED=1 go build -trimpath -o /tmp/jmdn-12cde77 . && /tmp/jmdn-12cde77 -version 2>/dev/null || strings /tmp/jmdn-12cde77 | grep -m1 'v2.0.1-'
sha256sum /tmp/jmdn-12cde77 | tee /tmp/jmdn-12cde77.sha256
docker build -t jmdn:12cde77 . && docker save jmdn:12cde77 | gzip > /home/testnet-user/jmdn-12cde77.tar.gz
go test ./messaging/... ./Sequencer/... ./Block/... 2>&1 | tail -5
```

Distribute: `scp /home/testnet-user/jmdn-12cde77.tar.gz <server>:/tmp/ && ssh <server>
'docker load < /tmp/jmdn-12cde77.tar.gz'` for each of the other 5 servers. Point the
compose files at `jmdn:12cde77` (`image:` line) — same tag everywhere.

## 4. Coordinated restart (runbook order)

Rule: same binary + same config everywhere; **stop sequencer → validators → seed;
start seed → validators → sequencer**. Pause bridge traffic (relayer) first.

```bash
# T0  testnet-seq: pause traffic, stop sequencer
systemctl stop jmdt-relayer 2>/dev/null; systemctl stop jmdn
# T1  every server: stop validators
docker compose stop $(docker ps --format '{{.Names}}' | grep -E '^jmdt-node-[0-9]+$')
# T2  seed: restart if its binary is also jmdn; otherwise leave running (confirm with colleague)
# T3  testnet-seq: install binary, apply daemon.json (restart docker while all validators are stopped)
install -m 755 /tmp/jmdn-12cde77 /usr/local/bin/jmdn && systemctl restart docker
# T4  every server: start validators on the new image (compose already edited), node-4 AWS included
docker compose up -d
for c in $(docker ps --format '{{.Names}}' | grep -E '^jmdt-node-[0-9]+$'); do docker logs $c 2>&1 | grep -m1 -oE '"version": "v[^"]+"'; done   # all identical
# T5  testnet-seq: start sequencer, resume traffic
systemctl start jmdn && sleep 20 && journalctl -u jmdn -n 50 --no-pager | grep -E "version|AVC beacon|INSTALLED|fallback recovery|ErrPartial|FATAL" | cut -c1-200
systemctl start jmdt-relayer 2>/dev/null
```

Every validator must log the same `v2.0.1-NNN-g12cde77` string; any node that logs
`ErrPartialVDFConfig`/exits has a VDF env mismatch — fix that node's env, do not
proceed with a mixed fleet.

## 5. Verification (first 30 blocks after restart)

```bash
# votes now counted against the full pool: denials should vanish, tally should show >7 voters
journalctl -u jmdn --since -30m --no-pager | grep -c "committee auth denied"            # expect ~0
journalctl -u jmdn --since -30m --no-pager | grep -E "tally|skipped_unauthorized|dropped_forgeries" | tail -3 | cut -c1-240
journalctl -u jmdn --since -30m --no-pager | grep -E "Consensus failed|BFT 2f\+1 not reached" | wc -l   # expect 0
# fleet sync
curl -s -X POST -H 'content-type: application/json' --data '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}' http://127.0.0.1:6545
# the 858 loop must stay dead
sleep 60; grep -c uq_txn_block_index /var/log/postgresql/postgresql-16-main.log         # expect 0 new lines
# node-4 AWS catching up (on AWS host)
docker logs --since 10m jmdt-node-4 2>&1 | grep -E "adopted a peer's VDF proof|state divergence|local tip" | tail -5
```

Then at block 1000: proof present, `entropy committee selected … "epoch":20` identical
on every node, no `fallback deadline exceeded`.

## 6. Rollback

Same procedure with `/usr/local/bin/jmdn` ← previous binary and `image: jmdn:cec52d4`
(tag the current image before §3: `docker tag <current image id> jmdn:cec52d4`).
No migrations ran, so rollback is binary-only. Keep `JMDN_AVC_VDF_*` exactly as is on
every node in either direction.

## 7. Filed for the colleague (code)

1. Unbounded 3 ms retry of `INSERT INTO transactions` on `uq_txn_block_index` (block 858)
   outside the bounded outbox — 2.4 GB/day of Postgres log, took the sequencer DB down.
2. `thebe_eventlog` slot inactive while the consumer errors → unbounded WAL retention.
3. node-4 divergence class: reward-recipient leaves differ after reproject/thebesync
   catch-up (31 accounts at block 912).
4. `adopted a peer's VDF proof` double log; stale `~1200-1410s` text (cosmetic).
