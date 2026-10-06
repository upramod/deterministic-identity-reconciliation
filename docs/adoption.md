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

The repository includes a generic SCIM Users adapter. Configure only the
attributes that the integrating project owns. The adapter reads the resource
before every mutation and defaults to setting active to false for
deprovisioning.

Subject lookup must establish either no match or exactly one match before the
adapter writes. The adapter requires a nonnegative `totalResults` in the SCIM
list response and rejects multiple matches, even when a server returns only
one resource on the current page. It also rejects missing resources when the
count indicates a match. These errors stop creation, updates, and
deprovisioning; they must not be treated as an absent account. This follows the
list-response contract in [RFC 7644, section 3.4.2](https://www.rfc-editor.org/rfc/rfc7644.html#section-3.4.2).

This check depends on truthful server metadata. It does not lock the remote
directory or make the lookup and subsequent write atomic.

When the matched resource includes `meta.version`, PATCH and DELETE send that
value as `If-Match`. A provider that enforces resource versions rejects a
concurrent change with HTTP 412; the adapter returns the error without an
unconditional retry. The caller must re-read source and target state before
deciding whether another reconciliation is appropriate. This is the standard
SCIM conditional-write mechanism, not a new reconciliation algorithm.

If the resource omits `meta.version`, writes remain unconditional. This adapter
does not discover version support or fetch an individual resource's ETag as a
fallback. It cannot guarantee concurrency safety for those providers, nor does
an ETag establish that the desired source state is current. Create-after-lookup
also remains subject to the provider's uniqueness constraints.

The lookup currently implements the RFC 7644 index-based list-response contract.
It does not support RFC 9865 cursor pagination, where `totalResults` can be
omitted. Such responses are rejected rather than treated as absent accounts.

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
