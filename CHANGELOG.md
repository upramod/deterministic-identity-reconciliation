# Changelog

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
