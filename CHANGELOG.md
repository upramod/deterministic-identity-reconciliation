# Changelog

## Unreleased

- Add a before-and-after evaluator for the four safety regressions, with
  source provenance and explicit expected failures on the pinned baseline.
- Reject empty or colliding normalized attribute names before projection.
- Preserve JSON payload numbers in the CLI without converting them to
  floating point, while continuing to reject trailing JSON content.
- Reject ambiguous or incomplete SCIM subject lookups before account writes.
  `totalResults` is required, including for empty results.
- Compare attribute key presence as well as values when deciding whether a
  target already matches. Different keys with empty values require a write.
- Added the permanent Zenodo DOI for the archived version 0.2.0 source release.

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
