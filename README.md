# Deterministic Identity Reconciliation

An open-source reference implementation for deterministic workforce identity
lifecycle reconciliation.

The engine treats incoming records as immutable facts. It canonicalizes those
facts, resolves temporal and revision conflicts, computes a desired identity
projection, and only then allows a target adapter to converge the account.

This repository is an engineering reference, not a complete identity
governance product. It contains no vendor credentials, production data, or
employer-specific implementation.

## Paper

This reference implementation accompanies:

**Pramod Ubbala, “Deterministic Temporal Reconciliation for Effective-Dated Identity Lifecycle Events,” arXiv:2609.14017, 2026.**

Paper: https://arxiv.org/abs/2609.14017

```bibtex
@misc{ubbala2026deterministic,
  title={Deterministic Temporal Reconciliation for Effective-Dated Identity Lifecycle Events},
  author={Ubbala, Pramod},
  year={2026},
  eprint={2609.14017},
  archivePrefix={arXiv},
  primaryClass={cs.SE}
}
```

## The problem

Enterprise identity systems rarely receive a clean command stream. They
receive physical records from HR, directories, and lifecycle systems. Those
records can be duplicated, corrected, delayed, or delivered out of order.

A direct message handler treats each record as an instruction:

```text
receive event -> mutate target account
```

That model is unsafe.

Consider this synthetic sequence:

| Event | Effective time | Delivery time |
| --- | --- | --- |
| Hire | January 1 | January 1 |
| Termination | January 25 | February 5 |
| Old profile update | January 20 | February 6 |

The profile update describes an earlier state, but it arrives after the
termination. A handler that applies messages in delivery order can write stale
attributes after the account should be disabled. Different delivery orders can
therefore leave different final states.

The required result is an inactive account. The late profile update must not
undo the termination.

This project uses a different boundary:

```text
physical events
    -> canonical facts
    -> freshness comparison
    -> desired identity state
    -> guarded target convergence
```

The resolver records the source fact, separates effective time from delivery
time, compares revisions using an explicit freshness tuple, and writes the
canonical projection with an atomic compare-and-set operation. The target
adapter reads the observed account before issuing a mutation.

The repository is a reference implementation of these control points. It is
not a complete identity product. It does not provide provider credentials,
production scheduling, deployment-specific policy, or a full audit archive.

## Quick start

Requires Go 1.22 or newer.

Run the tests:

```sh
go test ./...
```

Run the synthetic example at a fixed evaluation time:

```sh
go run ./cmd/reconcile -input examples/events.json -now 2026-09-12T00:00:00Z
```

The command prints one decision for each physical event and the resulting
desired projection for each subject.

## Reference contract

### Three clocks

- effective_time: when the business fact becomes true.
- modification_time: how fresh the source says the fact is.
- delivery time: when the reconciliation service received the record.

Delivery time is retained for audit. It never determines freshness.

### Freshness tuple

The reference comparator is:

```text
(source_sequence, modification_time, revision_number, physical_event_key)
```

The comparator is lexicographic. Source sequence wins first. Modification time
resolves equal sequence values. Revision number and the physical event key
make equal inputs deterministic.

Sources with different ordering semantics must provide a documented comparator.
Mixing comparators in one projection is unsafe.

### Source scopes

- hire_snapshot accepts hire and rehire.
- lifecycle accepts lifecycle changes other than hire and rehire.

A scope mismatch produces no_op. It does not mutate target state.

### Temporal decisions

The default policy accepts current and past facts, schedules future hires
outside the three-day hire window, and skips future lifecycle changes. It also
supports a cutoff date, a 48-hour retroactive window, and a three-day past-end
grace period.

These values are policy defaults. Deployments must review them against their
source and business rules.

## Architecture

```text
physical event
      |
      v
canonical fact  -- exact replay key and freshness tuple
      |
      v
desired projection  -- deterministic account state
      |
      v
compare-and-set persistence
      |
      v
read-before-write target adapter
```

The pure engine lives in pkg/reconcile. It has no database or network
dependency. PostgreSQL persistence lives in pkg/store/postgres.
The generic SCIM adapter lives in pkg/adapter/scim.

## PostgreSQL persistence

Apply the schema:

```sh
psql "$DATABASE_URL" -f schema/postgres.sql
```

The store uses database/sql and intentionally does not force a driver or
ORM. The application must register a PostgreSQL driver before opening its
connection. CompareAndSetProjection checks both the stored row version and
the incoming freshness tuple in one SQL statement.

## Target adapters

Implement reconcile.TargetAdapter:

1. Observe reads the target account.
2. The engine compares observed and desired state.
3. Apply runs only when state differs.

The first integration target is a generic SCIM-style account adapter. Provider
specific adapters should be added only after the generic contract has real
interoperability tests.

## Repository layout

```text
cmd/reconcile/          runnable JSON example
pkg/reconcile/           pure canonicalization and projection engine
pkg/adapter/scim/        read-before-write SCIM Users adapter
pkg/store/postgres/      database/sql persistence boundary
schema/                  PostgreSQL DDL
examples/                synthetic events
docs/                    public contract and adoption guide
```

## Non-goals

- Directly acting on a target account from a raw source event.
- Treating arrival order as business truth.
- Hiding source conflicts behind last-write-wins behavior.
- Claiming SCIM or IETF conformance without an interoperability test suite.
- Presenting this reference implementation as production-certified software.

## License

Apache License 2.0. See LICENSE.
