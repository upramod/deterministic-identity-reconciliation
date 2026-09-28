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

## Reproduce the four safety regressions

The unreleased fixes in [PR #6](https://github.com/upramod/deterministic-identity-reconciliation/pull/6)
include a before-and-after evaluator. It requires Python 3, Git, and Go 1.22
or newer. From the repository checkout:

```sh
git fetch origin pull/6/head
git switch --detach FETCH_HEAD
make evaluate > evaluation.json
```

The command exits successfully only when all four named regression groups fail
on the pinned baseline and pass on the candidate. It records the resolved
source commits and trees, Go version, commands, and observed test outcomes in
`evaluation.json`. Compiler errors or missing expected test results do not
count as reproduced defects.

The baseline is commit `23e702926408c789ce0129ce25dc6df93613d173`. The runner
exports the baseline and candidate to temporary directories, then copies the
candidate's regression test files into the baseline. It does not copy the
production fixes into the baseline or change the working checkout. Once source
commits and the Go toolchain are available, the evaluation needs no remote
provider, credentials, or production data.

To repeat a result against an exact recorded revision, use
`python3 scripts/evaluate.py --candidate <recorded-commit-SHA>`.
The [September 28 author-run report](evidence/safety-evaluation-2026-09-28.json)
records four expected failures on the baseline and four passing groups on
candidate `f768d0b6e25a2a2712d7fec57847bf89efe66d9f`. It is an example of the
report format, not an independent evaluation.

| Regression | Baseline failure | Corrected behavior |
| --- | --- | --- |
| Partial SCIM lookup | A partial page can trigger an account mutation without a unique match | Invalid or ambiguous result metadata stops the write |
| Empty-valued attribute keys | Different keys can compare equal and skip a needed write | Key presence participates in equivalence |
| Attribute-name collision | Distinct input keys can collapse after whitespace trimming | The event is rejected before projection |
| Large numeric identifier | Adjacent large integers can become the same value and hash | CLI decoding preserves exact numeric input |

Inspect the tests and JSON report before accepting the result. These are
synthetic, author-supplied regression cases, not a live-provider compatibility
test or evidence of adoption. An outside reviewer should add a source-specific
case and record any disagreement with the contract.

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
