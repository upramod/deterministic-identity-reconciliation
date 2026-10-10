# SCIM deprovision recovery

## Failure and correction

The default SCIM deprovision policy retains an account with `active=false`.
Previously, generic convergence compared that physical existence with desired
absence. Every subsequent convergence attempt therefore repeated the PATCH and
reported `updated`, even when the account was already disabled.

The SCIM adapter now supplies policy-aware state equivalence through the optional
`reconcile.TargetStateComparator` interface. In disable mode, either physical
absence or a disabled account completes deprovisioning. Retained attributes are
irrelevant to this decision. Delete mode still requires physical absence; desired
presence still uses the original comparison, including enabled state and
attributes. Existing adapters retain the original comparison by default.

## Reproduce

Requires Go 1.22 or newer and git history. From this candidate checkout:

```sh
go test ./pkg/adapter/scim -run 'TestConverge(Recovers|Deprovision)' -count=1 -v
go test -race ./...
go vet ./...

baseline_dir=$(mktemp -d)
git archive 17c74b9 | tar -x -C "$baseline_dir"
cp pkg/adapter/scim/recovery_test.go "$baseline_dir/pkg/adapter/scim/"
(cd "$baseline_dir" && go test ./pkg/adapter/scim -run 'TestConverge(Recovers|Deprovision)' -count=1 -v)
```

The final command is expected to fail on the baseline. The lost-response disable
case reports three mutations instead of one and two `updated` recovery results
instead of `unchanged`. The already-disabled default-mode case reports one write
instead of zero. Compilation errors are not successful reproduction.

The HTTP test server commits a disable or deletion and closes the connection
without sending the response. The first caller must receive an error. Each of
two subsequent attempts constructs a fresh adapter and observes the server's
retained state. The candidate produces exactly one total mutation and both
recovery attempts report `unchanged`. The observed resource remains physically
present in disable mode and absent in delete mode.

Additional cases check an active account, an already disabled account, physical
absence, delete of a disabled account, and rehire of a disabled account.

## Scope

This is a deterministic HTTP fault-injection test with an in-memory target. A
fresh adapter models loss of client-local state; it is not an operating-system
process crash or a PostgreSQL durability test. No live-provider interoperability,
external adoption, performance gain, or exactly-once distributed delivery is
claimed. It prevents repeat writes once a subsequent read sees the completed
deprovisioning. Eventually consistent reads, source-state fencing, and provider
uniqueness remain separate concerns. Callers must use current desired state on
recovery; this change does not make stale source state safe.
