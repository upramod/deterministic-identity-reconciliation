# External Evaluation Scenarios

This matrix lets an independent reviewer distinguish implemented, executable
behavior from lifecycle rules that remain outside version 0.1 of the reference
contract. Run the executable cases with `go test ./...`.

| Scenario | Expected invariant | Evidence in version 0.1 |
| --- | --- | --- |
| Duplicate delivery | An exact replay cannot cause a second transition | `TestCanonicalHashIgnoresDeliveryTime`; duplicate example fixture |
| Reordered delivery | Input permutation cannot change the desired projection | `TestResolveBatchIsPermutationInvariant` |
| Stale revision | The fresher source revision wins independently of arrival order | `TestOlderRevisionIsSkipped` |
| Equal-sequence correction | Revision number breaks an equal sequence and modification-time tie | `TestHigherRevisionBreaksEqualSequenceAndModificationTie` |
| Future hire | A near-term hire processes now; a later hire is scheduled | `TestFutureRules` |
| Future lifecycle event | A future non-hire cannot mutate current state | `TestFutureRules` |
| Rehire after termination | A later rehire restores an existing terminated identity to active | `TestRehireAfterTerminationRestoresActiveIdentity` |
| Leave and return | Leave retains and disables the identity; return re-enables it | `TestLeaveAndReturnFromLeavePreserveIdentity` |
| Retroactive correction | A recent correction may cross the cutoff; a stale one cannot | `TestCutoffDistinguishesStaleAndRecentCorrections` |
| Expired bounded fact | A fact beyond the past-end grace cannot alter current state | `TestPastEndGraceRejectsExpiredFact` |
| Read-before-write convergence | Equivalent target state produces no write | `TestReadBeforeWriteConvergence` |

## Explicit version 0.1 boundaries

The following production-oriented behaviors are not implemented by the pure
version 0.1 batch resolver and must not be inferred from the tests above:

- cancellation caused by disappearance from a later authoritative snapshot;
- durable scheduled-event removal;
- a distinct post-effective reversal event and durable reactivation record;
- source-specific sequence-zero custom-event precedence;
- termination timing based on the earlier of effective date and last date
  worked;
- cross-batch comparison against previously persisted event history.

Those behaviors require an explicit snapshot or persistence contract. Adding
them as test-only claims would overstate the current implementation. A later
contract version should define their event representation, audit semantics,
and state-transition rules before adding executable vectors.

## Reviewer procedure

1. Run `go test ./...`.
2. Read `docs/specification.md` and identify any ambiguous invariant.
3. Permute at least one fixture and confirm the projection remains identical.
4. Replace one fixture with sanitized source-specific data.
5. Record the repository commit, commands, observed results, and any defect.
