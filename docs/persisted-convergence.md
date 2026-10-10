# Apply a persisted identity projection

`cmd/converge` reads one subject from `canonical_state`, observes its SCIM
account, and previews or applies the difference. It does not ingest events,
schedule future work, or populate the table. The integrating application must
first persist a valid projection through the store API.

## Run

Configure the connection settings in the process environment:

- `IDENTITY_DATABASE_URL`: a PostgreSQL connection URL.
- `IDENTITY_SCIM_URL`: the provider's SCIM base URL.
- `IDENTITY_SCIM_TOKEN`: the provider bearer token, when required.

Use a database initialized with `schema/postgres.sql`. Only list scalar
attributes the integration owns. The command rejects any persisted attribute
that is absent from this explicit allowlist.

```sh
# Reads PostgreSQL and SCIM; does not write to the target.
go run ./cmd/converge -subject person-001 -managed-attributes displayName

# Apply the current persisted state, using disable as the deprovision policy.
go run ./cmd/converge -subject person-001 -managed-attributes displayName -apply
```

Optional flags: `-deprovision-mode delete` selects physical deletion instead of
disable; `-timeout 30s` sets the total deadline. A missing projection is an error
and never means permission to delete the account. No connection strings, tokens,
or attribute values appear in the command's success report.

The JSON report contains `subject_id`, `row_version`, `mode`, and `action`.
Preview reports `would_update` or `unchanged`; apply reports `updated` or
`unchanged`. Successful execution returns exit status zero; an error returns
nonzero. Output is an operation receipt, not a durable audit log.

## Locking and recovery

Apply uses `Store.WithLockedProjection`: a transaction selects the current
projection `FOR UPDATE`, then keeps that row lock until convergence and commit
finish. Another writer to the same row must wait, as must another command using
the same locked read. The command reads persisted state after acquiring the
lock, so it does not apply an old projection captured by an earlier attempt.

The lock spans network I/O. The operation deadline bounds that wait, and
callback failure rolls back and releases the lock. Keep deployment concurrency
and timeouts appropriate for the target. Preview uses an unlocked read and may
become stale; apply always reads again under its own lock.

If an application stops after committing a projection but before changing the
target, running this command applies the saved state. If a target write succeeds
but its response or the final report is lost, rerunning reads both current
persisted and target state before deciding whether another write is needed.
The database transaction cannot undo a remote write; a transaction or network
error must not be interpreted as proof that the remote state is unchanged.

This lock protects this database row, not upstream source systems. It does not
make PostgreSQL and SCIM one atomic system, prevent stale source events from
being persisted by another ingestion path, or guarantee safety when a provider
omits or ignores resource versions. It does not implement a worker queue,
lease scheduler, event-history resolver, or failover protocol.

## End-to-end validation

Configure a disposable PostgreSQL database as described in
[PostgreSQL recovery](postgres-recovery.md), install the pinned SCIM server as
described in [SCIM interoperability](scim-interoperability.md), and run:

```sh
python scripts/test-scim-interop.py --end-to-end
```

CI starts real PostgreSQL 16 and the unchanged independent SCIM server. The
test builds the actual command and executes a fresh process for every preview,
apply, and replay. It checks creation, preview without mutation, a persisted
termination deliberately left unapplied, recovery on the next invocation,
idempotent replay, rehire, unknown-attribute rejection, and missing-subject
rejection. Separate database tests verify that a competing writer cannot update
a locked projection and that callback failure releases the lock.

The test pauses work at the commit-to-apply boundary; it does not kill an
operating-system process or crash a database server. The independent server
stores test identities in memory. These are integration results, not external
adoption or a production-readiness certification.
