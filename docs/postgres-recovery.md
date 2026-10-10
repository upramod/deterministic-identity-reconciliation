# PostgreSQL persistence and recovery

## Contract defect

The original projection upsert checked expected row version only in its conflict
branch. When the row was absent, a caller expecting version 7 could insert a new
row at version 0. That reset the caller's version history despite the failed
precondition.

Positive expected versions now use a single conditional UPDATE. A missing row,
stale expected version, or non-newer freshness tuple returns false without a
write. The update still checks version and freshness atomically in PostgreSQL.
The Go package continues to accept `database/sql`; lib/pq is a test dependency.

Version zero retains the existing bootstrap convention: it can create a missing
row at version zero or advance an existing version-zero row. It does not mean
create-only. Callers needing a distinct absent-row token need a separate API
contract. This change also does not prevent an ABA problem if an external actor
deletes and recreates rows with reused versions.

## Run against a real database

Use a disposable PostgreSQL database and a role allowed to create schemas:

```sh
export IDENTITY_TEST_DATABASE_URL='postgres://identity_test:local_test_only@localhost:5432/identity_test?sslmode=disable'
go test -race ./pkg/store/postgres -run TestPostgres -count=1 -v
```

Each test creates an isolated schema using `schema/postgres.sql` and removes it
after execution. The dedicated CI job starts PostgreSQL 16 and sets the URL.
Ordinary local tests skip these cases when the variable is absent; such a run is
not database validation. The integration suite checks:

- A missing row stays absent when a caller expects a positive version.
- Closing the database pool and opening a new one preserves a terminated
  projection, its version, freshness, and attributes.
- Replaying that committed projection does not advance the row version.
- Stale row versions and stale source revisions cannot change persisted state.
- Two workers on separate pools, both expecting the same positive version,
  produce exactly one successful update; persisted state matches that winner.
- Newer valid writes advance the version, with each freshness tuple component
  checked in both directions against the Go comparison.
- Duplicate physical event keys cannot replace the original payload.

## Reproduce the original failure

The test-only commit `b654c8bca4b7d7dc6be6fc8c0c23d009764e8184`
contains the uncorrected production code and the database regression:

```sh
baseline_dir=$(mktemp -d)
git archive b654c8bca4b7d7dc6be6fc8c0c23d009764e8184 | tar -x -C "$baseline_dir"
(cd "$baseline_dir" && go test ./pkg/store/postgres -run TestPostgresCASMissingExpectedVersion -count=1 -v)
```

Keep `IDENTITY_TEST_DATABASE_URL` set. Expected failure:
`missing-row CAS unexpectedly created a projection`. A skipped test, connection
failure, or compilation error does not reproduce the defect.

## Limits

These tests use the production SQL and a real database. Reopening a client pool
does not simulate a database server crash, storage loss, failover, or a network
partition. The replay case models a caller repeating a committed write; it does
not inject packet loss into the PostgreSQL protocol. There is no atomic
transaction spanning PostgreSQL and a SCIM provider. Cross-batch event history,
durable work scheduling, and source-state fencing remain separate work.
