# Changelog

## 0.2.0 - 2026-09-23

- Added executable evaluation cases for rehire after termination, leave and
  return, retroactive cutoff handling, past-end grace, and correction
  precedence.
- Added an external evaluation matrix that maps lifecycle invariants to tests.
- Added an independent technical review guide with reproducibility steps and
  focused review questions.
- Added a structured GitHub issue template for independent review records.
- Added citation metadata for the software and accompanying arXiv paper.
- Documented version 0.1 contract boundaries, including snapshot
  disappearance, durable schedule cancellation, distinct reversal records,
  custom sequence-zero precedence, source-specific termination timing, and
  cross-batch persisted history.

This release expands evaluation and review material. It does not claim
production certification, standards conformance, or implementation of the
documented contract boundaries.

## 0.1.0 - Initial reference implementation

- Added deterministic event canonicalization.
- Added freshness tuple comparison.
- Added source-scope and temporal policy decisions.
- Added permutation-invariant desired projections.
- Added read-before-write target convergence.
- Added PostgreSQL schema and database/sql persistence boundary.
- Added a generic SCIM Users adapter with read-before-write behavior.
- Added synthetic fixtures, tests, and CI.
- Termination projections use secure deactivation by default; physical
  deletion remains an explicit target policy.
