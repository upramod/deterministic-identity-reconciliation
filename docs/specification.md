# Deterministic Reconciliation Contract

Version: 0.1

## 1. Purpose

The contract defines how a workforce identity system turns unreliable delivery
of lifecycle records into a stable desired state. It separates the source
record, the canonical fact, the durable projection, and the target mutation.

The contract does not define a vendor API. It defines the semantic boundary
that a source adapter, state store, and target adapter must preserve.

## 2. Physical event

A physical event contains:

- a globally unique event_key;
- a stable subject_id;
- an event_type;
- a source_scope;
- an effective_time;
- a modification_time;
- a non-negative source_sequence;
- a non-negative revision_number;
- an optional end_time;
- a JSON payload.

The event is immutable after ingestion. A correction is a new physical event,
not an in-place edit.

## 3. Canonical fact

Canonicalization trims identifiers, normalizes event and scope values, converts
timestamps to UTC, extracts scalar target attributes, and computes a SHA-256
payload hash.

Attribute names are trimmed. An empty trimmed name or two distinct input names
that trim to the same name makes the event invalid. This applies even when the
values match or a value is non-scalar and would otherwise be ignored. Rejecting
the whole event prevents map iteration order from selecting a target attribute.
Canonicalization examines input names in lexical order so errors are stable.
Distinct, nonempty trimmed names retain the existing scalar-value extraction
rules; whitespace around an otherwise unambiguous name remains accepted.

The CLI preserves JSON payload numbers as `json.Number`. Numeric attributes
retain their input spelling instead of passing through `float64`; large
integer identifiers therefore remain exact. This also means `1`, `1.0`, and
`1e0` produce distinct attribute strings and payload hashes. Sources must use
a stable numeric representation for exact replay, or encode identifiers as
strings. Library callers that decode their own input must preserve numeric
precision before calling the engine.

The delivery clock is attached during canonicalization as received_at. It is
not part of the payload hash or the freshness tuple.

The logical fact key is:

```text
(subject_id, source_scope, event_type, effective_time)
```

Different physical records with the same logical key are revisions of one
fact. Exact replay is detected first by event_key.

## 4. Revision resolution

The reference freshness tuple is:

```text
(source_sequence, modification_time, revision_number, physical_event_key)
```

Comparison is lexicographic. A candidate replaces the current fact only when
its tuple is greater. Equal tuples cannot produce a second state transition.

The source sequence comes first because this reference assumes a monotonic
source sequence. A source that gives modification time primary authority must
use a separate documented comparator and separate persistence semantics.

## 5. Decision policy

The policy produces one of four actions:

- process_now: apply the fact to the current desired projection;
- schedule: retain a future hire for later dispatch;
- skip: retain an audit decision without changing state;
- no_op: the record does not belong to the source scope.

The default policy:

- accepts past and current events;
- accepts future hires within 72 hours;
- schedules future hires beyond 72 hours;
- skips future lifecycle changes;
- skips facts whose end time is more than 72 hours old;
- skips pre-cutoff facts unless their modification time falls within the
  48-hour retroactive window.

The policy is evaluated only after revision resolution. An older revision must
not win merely because it has a more convenient effective date.

## 6. Projection

Facts are applied to projections in ascending effective_time. Equal effective
times use the freshness tuple, event type, and event key as stable tie-breakers.

The projection contains:

- target account existence;
- enabled state;
- lifecycle state;
- target attributes;
- source fact and event provenance;
- the winning freshness tuple;
- a durable row version.

Hire and rehire create an active identity. Leave disables the identity while
retaining it. Return from leave re-enables it. Termination disables the desired
account and marks the lifecycle state as terminated. Data updates, transfers,
and conversions change attributes but do not infer account existence. Physical
deletion, if required by a target, belongs to an explicit provider policy.

## 7. Target convergence

An adapter must implement a read-before-write boundary:

1. Read the target state.
2. Compare target-observable fields with the desired projection.
3. Do nothing when the states are equal.
4. Mutate only when the states differ.

An HTTP success response is not proof of convergence. The adapter must verify
the target representation on the next observation.

## 8. Durable compare-and-set

The PostgreSQL state mutation must check:

- the expected row_version;
- the incoming freshness tuple is newer than the stored tuple.

The update increments row_version in the same statement. A zero-row result
means that another writer advanced the row or the incoming fact is stale.
Callers must reload the projection and re-evaluate rather than force an
overwrite.

## 9. Required conformance properties

An implementation claiming compatibility with this contract should test:

- permutation invariance;
- exact replay idempotence;
- revision dominance;
- deterministic tie-breaking;
- future-event policy;
- cutoff and retroactive behavior;
- compare-and-set conflict handling;
- read-before-write target convergence.
