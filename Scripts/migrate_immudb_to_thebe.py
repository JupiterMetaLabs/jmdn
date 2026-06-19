#!/usr/bin/env python3
"""
migrate_immudb_to_thebe.py
──────────────────────────
Reads all blocks, transactions, and accounts from a live ImmuDB instance
and writes them directly into the ThebeDB Postgres schema.

No JMDN, no Go, no ThebeDB Go client required — just:
  pip install immudb-py psycopg2-binary

USAGE:
  python3 migrate_immudb_to_thebe.py [OPTIONS]

OPTIONS:
  --immudb-host    ImmuDB host          (default: 127.0.0.1)
  --immudb-port    ImmuDB port          (default: 3322)
  --immudb-user    ImmuDB username      (default: immudb)
  --immudb-pass    ImmuDB password      (default: immudb)
  --main-db        ImmuDB main DB name  (default: defaultdb)
  --accounts-db    ImmuDB accounts DB   (default: accountsdb)
  --pg-dsn         Postgres DSN         (default: postgres://jmdn:jmdndefault@127.0.0.1:5430/jmdn)
  --batch-size     Keys per scan batch  (default: 500)
  --start-block    Resume from block N  (default: 0)
  --skip-blocks    Skip block/tx migration
  --skip-accounts  Skip account migration
  --dry-run        Read only, no writes

SCHEMA TABLES WRITTEN:
  accounts, blocks, snapshots, transactions, zk_proofs
"""

import argparse
import base64
import json
import sys
import time
import traceback
from datetime import datetime, timezone

try:
    import psycopg2
    import psycopg2.extras
except ImportError:
    sys.exit("Missing psycopg2 — run: pip install psycopg2-binary")

try:
    from immudb import ImmudbClient
except ImportError:
    sys.exit("Missing immudb-py — run: pip install immudb-py")


# ── CLI ───────────────────────────────────────────────────────────────────────

def parse_args():
    p = argparse.ArgumentParser(description="Migrate ImmuDB → ThebeDB Postgres")
    p.add_argument("--immudb-host",   default="127.0.0.1")
    p.add_argument("--immudb-port",   default=3322, type=int)
    p.add_argument("--immudb-user",   default="immudb")
    p.add_argument("--immudb-pass",   default="immudb")
    p.add_argument("--main-db",       default="defaultdb")
    p.add_argument("--accounts-db",   default="accountsdb")
    p.add_argument("--pg-dsn",        default="postgres://jmdn:jmdndefault@127.0.0.1:5430/jmdn")
    p.add_argument("--batch-size",    default=500, type=int)
    p.add_argument("--start-block",   default=0, type=int)
    p.add_argument("--skip-blocks",   action="store_true")
    p.add_argument("--skip-accounts", action="store_true")
    p.add_argument("--dry-run",       action="store_true")
    p.add_argument("--list-dbs",      action="store_true",
                   help="List available ImmuDB databases and exit")
    return p.parse_args()


# ── Logging ───────────────────────────────────────────────────────────────────

def log(msg):
    ts = datetime.now(timezone.utc).strftime("%H:%M:%S")
    print(f"[{ts}] {msg}", flush=True)

def warn(msg):
    print(f"[WARN] {msg}", flush=True, file=sys.stderr)


# ── ImmuDB connection ─────────────────────────────────────────────────────────

def immudb_connect(host, port, user, password, db_name):
    client = ImmudbClient(f"{host}:{port}")
    client.login(user, password)
    client.useDatabase(db_name.encode())
    log(f"ImmuDB connected → {host}:{port} / {db_name}")
    return client


def immudb_get(client, key: str):
    """Get a single key value. Returns bytes or None."""
    try:
        result = client.get(key.encode())
        return result.value if result else None
    except Exception:
        return None


def immudb_scan_prefix(client, prefix: str, batch_size: int):
    """
    Yield (key_bytes, value_bytes) for all keys matching prefix.
    Paginates via seekKey.
    """
    seek = b""
    prefix_b = prefix.encode()
    while True:
        try:
            result = client.scan(seek, prefix_b, False, batch_size)
        except Exception as e:
            warn(f"scan error (prefix={prefix!r}, seek={seek!r}): {e}")
            break

        entries = result if isinstance(result, dict) else getattr(result, "entries", {})
        if not entries:
            break

        # immudb-py returns dict {key: value} or a ScanResponse object
        if isinstance(entries, dict):
            items = list(entries.items())
        else:
            items = [(e.key, e.value) for e in entries]

        if not items:
            break

        for k, v in items:
            yield k, v

        last_key = items[-1][0]
        if len(items) < batch_size:
            break
        seek = last_key


# ── Postgres helpers ──────────────────────────────────────────────────────────

def pg_connect(dsn: str):
    conn = psycopg2.connect(dsn)
    conn.autocommit = False
    log(f"Postgres connected → {dsn.split('@')[-1]}")
    return conn


def ensure_schema(conn):
    """Create tables if they don't exist (idempotent DDL from thebeprofile)."""
    ddl = """
    CREATE TABLE IF NOT EXISTS accounts (
        address      CHAR(42)     PRIMARY KEY,
        did_address  TEXT         NOT NULL UNIQUE,
        balance_wei  VARCHAR(30)  NOT NULL DEFAULT '0',
        nonce        VARCHAR(30)  NOT NULL DEFAULT '0',
        account_type SMALLINT     NOT NULL DEFAULT 0,
        metadata     JSONB        NOT NULL DEFAULT '{}'::jsonb,
        created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
        updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
    );

    CREATE TABLE IF NOT EXISTS blocks (
        block_number  BIGINT       PRIMARY KEY,
        block_hash    CHAR(66)     NOT NULL UNIQUE,
        parent_hash   CHAR(66)     NOT NULL,
        timestamp     TIMESTAMPTZ  NOT NULL,
        txs_root      CHAR(66)     NOT NULL,
        state_root    CHAR(66)     NOT NULL,
        logs_bloom    BYTEA,
        coinbase_addr CHAR(42),
        zkvm_addr     CHAR(42),
        gas_limit     NUMERIC(78,0),
        gas_used      NUMERIC(78,0),
        status        SMALLINT     NOT NULL DEFAULT 1,
        extra_data    JSONB        NOT NULL DEFAULT '{}'::jsonb,
        created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW()
    );

    CREATE TABLE IF NOT EXISTS snapshots (
        block_number BIGINT      PRIMARY KEY,
        block_hash   CHAR(66)    NOT NULL UNIQUE,
        created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        CONSTRAINT fk_snapshot_block
            FOREIGN KEY (block_number) REFERENCES blocks(block_number)
    );

    CREATE TABLE IF NOT EXISTS transactions (
        tx_hash              CHAR(66)      PRIMARY KEY,
        block_number         BIGINT        NOT NULL,
        tx_index             SMALLINT      NOT NULL,
        from_addr            CHAR(42)      NOT NULL,
        to_addr              CHAR(42),
        value_wei            NUMERIC(78,0) NOT NULL DEFAULT 0,
        nonce                NUMERIC(78,0) NOT NULL DEFAULT 0,
        type                 SMALLINT      NOT NULL DEFAULT 0,
        gas_limit            VARCHAR(30),
        gas_price_wei        VARCHAR(30),
        max_fee_wei          VARCHAR(30),
        max_priority_fee_wei VARCHAR(30),
        data                 BYTEA,
        access_list          JSONB         NOT NULL DEFAULT '[]'::jsonb,
        sig_v                BIGINT        NOT NULL DEFAULT 0,
        sig_r                CHAR(66)      NOT NULL DEFAULT '0x' || repeat('0',64),
        sig_s                CHAR(66)      NOT NULL DEFAULT '0x' || repeat('0',64),
        created_at           TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
        CONSTRAINT fk_txn_snapshot
            FOREIGN KEY (block_number) REFERENCES snapshots(block_number),
        CONSTRAINT uq_txn_block_index UNIQUE (block_number, tx_index)
    );

    CREATE TABLE IF NOT EXISTS zk_proofs (
        block_number BIGINT      PRIMARY KEY,
        proof_hash   CHAR(66)    NOT NULL UNIQUE,
        stark_proof  BYTEA       NOT NULL,
        commitment   BYTEA,
        created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        CONSTRAINT fk_zkproof_block
            FOREIGN KEY (block_number) REFERENCES blocks(block_number)
    );
    """
    with conn.cursor() as cur:
        cur.execute(ddl)
    conn.commit()
    log("Schema ready")


# ── Data helpers ──────────────────────────────────────────────────────────────

def to_ts(value) -> datetime:
    """Convert Unix seconds/millis/nanos or ISO string to aware datetime."""
    if not value:
        return datetime.now(timezone.utc)
    if isinstance(value, str):
        try:
            return datetime.fromisoformat(value.replace("Z", "+00:00"))
        except Exception:
            return datetime.now(timezone.utc)
    # Numeric: detect scale by magnitude
    v = int(value)
    if v > 1_000_000_000_000_000:   # nanoseconds
        v //= 1_000_000_000
    elif v > 1_000_000_000_000:     # milliseconds
        v //= 1_000
    return datetime.fromtimestamp(v, tz=timezone.utc)


def hex_pad(h, length=66) -> str:
    """Normalise a hex hash/address to lowercase 0x-prefixed padded form."""
    if not h:
        return "0x" + "0" * (length - 2)
    s = str(h).lower()
    if not s.startswith("0x"):
        s = "0x" + s
    if len(s) < length:
        s = "0x" + s[2:].zfill(length - 2)
    return s[:length]


def to_bytes(v) -> bytes:
    """Convert a JSON bytes field to Python bytes.
    Handles: list[int], hex string (0x...), base64 string, raw bytes."""
    if v is None:
        return b""
    if isinstance(v, (bytes, bytearray)):
        return bytes(v)
    if isinstance(v, list):
        return bytes(int(x) & 0xFF for x in v)
    s = str(v)
    if s.startswith("0x") or s.startswith("0X"):
        h = s[2:]
        if len(h) % 2:
            h = "0" + h
        return bytes.fromhex(h)
    try:
        return base64.b64decode(s)
    except Exception:
        return s.encode()


def bigint_str(v) -> str:
    """Convert a big.Int hex/decimal/None to decimal string."""
    if v is None:
        return "0"
    if isinstance(v, int):
        return str(v)
    s = str(v).strip()
    if s.startswith("0x") or s.startswith("0X"):
        try:
            return str(int(s, 16))
        except Exception:
            return "0"
    return s if s.isdigit() or (s.startswith("-") and s[1:].isdigit()) else "0"


# ── Block migration ───────────────────────────────────────────────────────────

def migrate_blocks(immu_main, pg_conn, start_block: int, batch_size: int, dry_run: bool):
    # Get latest block number
    raw = immudb_get(immu_main, "latest_block")
    if not raw:
        log("No latest_block key found — nothing to migrate")
        return
    latest = int(raw.decode().strip())
    log(f"Blocks: {start_block} → {latest} ({latest - start_block + 1} total)")

    migrated, skipped, failed = 0, 0, 0

    cur = pg_conn.cursor()

    for block_num in range(start_block, latest + 1):
        raw = immudb_get(immu_main, f"block:{block_num}")
        if not raw:
            skipped += 1
            continue

        try:
            b = json.loads(raw)
        except Exception as e:
            warn(f"block:{block_num} JSON parse error: {e}")
            failed += 1
            continue

        block_hash   = hex_pad(b.get("blockhash", ""), 66)
        parent_hash  = hex_pad(b.get("prevhash", ""), 66)
        state_root   = hex_pad(b.get("stateroot", ""), 66)
        txs_root     = hex_pad(b.get("txnsroot", ""), 66) if b.get("txnsroot") else hex_pad("", 66)
        ts           = to_ts(b.get("timestamp"))
        coinbase     = hex_pad(b.get("coinbaseaddr", ""), 42) if b.get("coinbaseaddr") else None
        zkvm         = hex_pad(b.get("zkvmaddr", ""), 42) if b.get("zkvmaddr") else None
        logs_bloom   = to_bytes(b.get("logsbloom")) or None
        gas_limit    = b.get("gaslimit", 0)
        gas_used     = b.get("gasused", 0)
        proof_hash   = hex_pad(b.get("proof_hash", ""), 66)
        stark_proof  = to_bytes(b.get("starkproof"))
        commitment   = to_bytes(b.get("commitment")) or None
        # Sentinel/placeholder proof_hash — all same nibble (0xcccc..., 0x0000..., etc.)
        _ph_body = proof_hash[2:] if proof_hash.startswith("0x") else proof_hash
        has_real_proof = len(set(_ph_body)) > 1
        extra_data   = json.dumps({"extradata": b.get("extradata", "")})
        status_str   = b.get("status", "confirmed")
        status       = 1 if status_str in ("confirmed", "1", 1, True) else 0
        transactions = b.get("transactions", [])

        if not dry_run:
            try:
                # 1. Insert block
                cur.execute("""
                    INSERT INTO blocks
                        (block_number, block_hash, parent_hash, timestamp, txs_root,
                         state_root, logs_bloom, coinbase_addr, zkvm_addr,
                         gas_limit, gas_used, status, extra_data)
                    VALUES (%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s)
                    ON CONFLICT (block_number) DO NOTHING
                """, (block_num, block_hash, parent_hash, ts, txs_root,
                      state_root, logs_bloom, coinbase, zkvm,
                      gas_limit, gas_used, status, extra_data))

                # 2. Insert snapshot
                cur.execute("""
                    INSERT INTO snapshots (block_number, block_hash)
                    VALUES (%s,%s)
                    ON CONFLICT (block_number) DO NOTHING
                """, (block_num, block_hash))

                # 3. Insert transactions
                for idx, tx in enumerate(transactions):
                    _insert_tx(cur, tx, block_num, idx)

                pg_conn.commit()
                migrated += 1

                # ZK proof — separate commit so a duplicate proof_hash never
                # rolls back the block/snapshot/transactions above
                if has_real_proof:
                    try:
                        cur.execute("""
                            INSERT INTO zk_proofs
                                (block_number, proof_hash, stark_proof, commitment)
                            VALUES (%s,%s,%s,%s)
                            ON CONFLICT DO NOTHING
                        """, (block_num, proof_hash, stark_proof, commitment))
                        pg_conn.commit()
                    except Exception:
                        pg_conn.rollback()
                        # non-fatal: block already committed

            except Exception as e:
                pg_conn.rollback()
                warn(f"block:{block_num} write error: {e}")
                failed += 1
                continue
        else:
            migrated += 1

        if (block_num - start_block + 1) % 500 == 0:
            log(f"  blocks: {migrated} migrated, {skipped} skipped, {failed} failed (at {block_num})")

    cur.close()
    log(f"Blocks done: {migrated} migrated, {skipped} skipped, {failed} failed")


def _insert_tx(cur, tx: dict, block_number: int, tx_index: int):
    tx_hash  = hex_pad(tx.get("hash", ""), 66)
    from_a   = hex_pad(tx.get("from", ""), 42)
    to_raw   = tx.get("to")
    to_a     = hex_pad(to_raw, 42) if to_raw else None
    value    = bigint_str(tx.get("value"))
    nonce    = str(tx.get("nonce", 0))
    tx_type  = int(tx.get("type", 0))
    gas      = str(tx.get("gas_limit", 0))
    gp       = bigint_str(tx.get("gas_price"))
    mf       = bigint_str(tx.get("max_fee"))
    mpf      = bigint_str(tx.get("max_priority_fee"))
    data_b   = to_bytes(tx.get("data")) or None
    acl      = json.dumps(tx.get("access_list", []))
    v        = int(bigint_str(tx.get("v")) or 0)
    r        = hex_pad(bigint_str(tx.get("r")), 66)
    s        = hex_pad(bigint_str(tx.get("s")), 66)

    cur.execute("""
        INSERT INTO transactions
            (tx_hash, block_number, tx_index, from_addr, to_addr, value_wei,
             nonce, type, gas_limit, gas_price_wei, max_fee_wei,
             max_priority_fee_wei, data, access_list, sig_v, sig_r, sig_s)
        VALUES (%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s)
        ON CONFLICT (tx_hash) DO NOTHING
    """, (tx_hash, block_number, tx_index, from_a, to_a, value,
          nonce, tx_type, gas, gp, mf, mpf, data_b, acl, v, r, s))


# ── Account migration ─────────────────────────────────────────────────────────

def migrate_accounts(immu_accounts, pg_conn, batch_size: int, dry_run: bool):
    log("Scanning accounts (prefix: address:)...")
    migrated, failed = 0, 0
    cur = pg_conn.cursor()

    for key_b, val_b in immudb_scan_prefix(immu_accounts, "address:", batch_size):
        try:
            acc = json.loads(val_b)
        except Exception as e:
            warn(f"account {key_b!r} JSON parse error: {e}")
            failed += 1
            continue

        address = acc.get("address", "")
        if isinstance(address, dict):  # common.Address serialized as hex object
            address = address.get("hex", "") or address.get("address", "")
        address = hex_pad(str(address), 42)

        did     = acc.get("did", "") or acc.get("did_address", "") or address
        balance = str(acc.get("balance", "0") or "0")
        nonce   = str(acc.get("nonce", 0))
        acc_type = 1 if acc.get("account_type") == "publickey" else 0
        meta    = json.dumps(acc.get("metadata") or {})
        created = to_ts(acc.get("created_at"))
        updated = to_ts(acc.get("updated_at"))

        if not dry_run:
            try:
                cur.execute("""
                    INSERT INTO accounts
                        (address, did_address, balance_wei, nonce, account_type,
                         metadata, created_at, updated_at)
                    VALUES (%s,%s,%s,%s,%s,%s,%s,%s)
                    ON CONFLICT (address) DO UPDATE SET
                        balance_wei  = EXCLUDED.balance_wei,
                        nonce        = EXCLUDED.nonce,
                        updated_at   = EXCLUDED.updated_at
                """, (address, did, balance, nonce, acc_type, meta, created, updated))
                pg_conn.commit()
                migrated += 1
            except Exception as e:
                pg_conn.rollback()
                warn(f"account {address} write error: {e}")
                failed += 1
        else:
            migrated += 1

        if migrated % 1000 == 0:
            log(f"  accounts: {migrated} migrated so far…")

    cur.close()
    log(f"Accounts done: {migrated} migrated, {failed} failed")


# ── Main ──────────────────────────────────────────────────────────────────────

def main():
    args = parse_args()

    # --list-dbs: connect to ImmuDB, print databases, exit
    if args.list_dbs:
        client = ImmudbClient(f"{args.immudb_host}:{args.immudb_port}")
        client.login(args.immudb_user, args.immudb_pass)
        dbs = client.listDatabases()
        print("Available ImmuDB databases:")
        for db in dbs:
            name = db.databaseName if hasattr(db, "databaseName") else str(db)
            print(f"  {name}")
        return

    if args.dry_run:
        log("DRY RUN — no writes will be performed")

    # Postgres
    pg = pg_connect(args.pg_dsn)
    if not args.dry_run:
        ensure_schema(pg)

    # ImmuDB — main DB (blocks + txs)
    if not args.skip_blocks:
        immu_main = immudb_connect(
            args.immudb_host, args.immudb_port,
            args.immudb_user, args.immudb_pass,
            args.main_db,
        )

    # ImmuDB — accounts DB
    if not args.skip_accounts:
        immu_accounts = immudb_connect(
            args.immudb_host, args.immudb_port,
            args.immudb_user, args.immudb_pass,
            args.accounts_db,
        )

    start = time.time()

    # Accounts first — transactions FK-reference account addresses
    if not args.skip_accounts:
        log("── Migrating accounts ──")
        migrate_accounts(immu_accounts, pg, args.batch_size, args.dry_run)

    if not args.skip_blocks:
        log("── Migrating blocks + transactions ──")
        migrate_blocks(immu_main, pg, args.start_block, args.batch_size, args.dry_run)

    elapsed = time.time() - start
    log(f"Migration complete in {elapsed:.1f}s")
    pg.close()


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        log("Interrupted")
        sys.exit(1)
    except Exception:
        traceback.print_exc()
        sys.exit(1)
