# Integration Context

JMDN persists chain data through **Cassata**, which is the single storage adapter in this repository.

## Storage Topology

- ThebeDB receives append records from Cassata.
- PostgreSQL holds the queryable projection (accounts, blocks, transactions, proofs, snapshots).
- Redis is used by ThebeDB runtime components.

## Local Development Wiring

- SQL DSN: `postgres://thebedb:thebedb@localhost:5432/thebedb_test?sslmode=disable`
- Redis URL: `redis://localhost:6379`

Bring dependencies up with:

```bash
docker compose up -d
```

Optional tooling:

- `pgadmin` is available behind the `tools` profile:
  - `docker compose --profile tools up -d`
