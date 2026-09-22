# Independent Technical Review Guide

This guide is for identity, security, and distributed-systems practitioners
who want to evaluate the deterministic reconciliation contract without access
to a production environment or private data.

The review should assess the public artifact on its technical merits. A useful
review identifies what was examined, what reproduced, what failed, and where
the approach would or would not apply. Agreement with the author is not
expected.

## Artifact under review

- Paper: [Deterministic Temporal Reconciliation for Effective-Dated Identity
  Lifecycle Events](https://arxiv.org/abs/2609.14017)
- Reference implementation: this repository
- Contract: [`specification.md`](specification.md)
- Executable cases: [`evaluation-scenarios.md`](evaluation-scenarios.md)
- Adoption boundary: [`adoption.md`](adoption.md)

The repository contains synthetic data only. Do not submit credentials,
personal data, production logs, or confidential source records.

## Reproduce the reference behavior

Requirements: Go 1.22 or newer.

```sh
git clone https://github.com/upramod/deterministic-identity-reconciliation.git
cd deterministic-identity-reconciliation
go test ./...
go run ./cmd/reconcile -input examples/events.json -now 2026-09-12T00:00:00Z
```

Record the commit SHA and Go version with the result:

```sh
git rev-parse HEAD
go version
```

## Core review questions

1. Does the freshness tuple produce deterministic results under reordered and
   duplicated delivery?
2. Do the effective-time and cutoff rules prevent stale lifecycle facts from
   changing current desired state?
3. Does the projection model preserve the required distinction between source
   facts, current desired state, and target mutations?
4. Are the version 0.1 boundaries stated accurately, or does the repository
   imply behavior it does not implement?
5. Does this contract address a material failure mode in workforce identity
   systems you have designed, operated, studied, or reviewed?
6. What source semantics or deployment conditions would make the reference
   comparator unsafe?
7. Could any part of the contract, test vectors, or implementation be reused
   in another identity system? If so, identify the part and intended use.

## Optional independent test

Create one sanitized fixture that represents a real class of lifecycle problem
without copying private data. Useful cases include:

- a termination followed by a late profile update;
- a corrected effective date;
- duplicate delivery after a target timeout;
- a rehire after termination;
- leave followed by return from leave;
- two conflicting revisions of the same logical fact.

Run the fixture in at least two different input orders. Compare the complete
projection, not only the final enabled flag.

## Review record

A reviewer may open a GitHub issue using the independent technical review
template or provide the same information in another written record:

- reviewer name and professional role;
- relevant identity, security, or distributed-systems experience;
- repository commit and environment;
- commands or scenarios examined;
- reproduced results;
- defects, limitations, or disputed claims;
- relevance, if any, to systems outside this repository;
- any reuse, pilot, adaptation, or citation.

Public review is preferred when the reviewer is comfortable publishing it.
Private feedback is still useful for correcting the implementation, but it
does not create a public verification record.

## What this review does not establish

A passing test run does not prove production safety, standards conformance, or
fitness for a specific deployment. Repository stars, empty forks, and general
endorsements do not show technical use. Strong evidence requires a reproducible
evaluation, a documented pilot, a merged integration, an adaptation, or a
specific technical assessment.
