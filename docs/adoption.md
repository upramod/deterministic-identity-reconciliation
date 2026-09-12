# Adoption Guide

This guide is for maintainers evaluating the reference implementation.

## Start with the test vectors

Run the example and inspect the decisions. Replace the synthetic events with a
sanitized fixture from the integrating project. Do not send production
credentials or personal data to this repository.

The integrating project should compare its current behavior with the
reference decisions for:

1. duplicated delivery;
2. reordered delivery;
3. stale revisions;
4. future hires;
5. future terminations;
6. a target timeout followed by replay.

## Integrate in a narrow boundary

Use the pure package at the source-to-projection boundary. Keep provider API
calls in a target adapter. Keep scheduling outside the pure resolver.

A first integration should be small enough to review as one pull request. The
integration must document:

- the source comparator;
- the logical fact key;
- the target fields used for equivalence;
- the rollback behavior;
- the metrics collected before and after adoption.

## Evidence of real use

The useful public signals are:

- a merged integration pull request;
- a released dependency or plugin;
- an independent conformance report;
- a documented pilot;
- a citation that names the contract or test vectors;
- an issue or design review that records an independent maintainer's
  technical assessment.

Repository stars and forks show interest. They do not show that the resolver
was used or trusted.

## Maintainer checklist

- [ ] Review docs/specification.md.
- [ ] Run go test ./...
- [ ] Add source-specific fixtures.
- [ ] Confirm the freshness comparator.
- [ ] Add target adapter equivalence tests.
- [ ] Run a replay test before enabling writes.
- [ ] Record the version used by the integrating system.
- [ ] Report defects as issues with a minimal reproducible fixture.
