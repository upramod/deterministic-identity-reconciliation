# SCIM conditional-write review

This is a bounded review of PR #7, not a claim of independent validation.

## Question

Does sending the matched resource's meta.version as If-Match prevent this adapter from overwriting a target change between lookup and PATCH or DELETE, when the provider enforces versions?

Candidate code: e6545642902ff429702590190bd37d26f176a789.
Baseline: a1e04d86ace895789b1544276bd84a753a1dfdcf.

## Reproduce without modifying your checkout

Requires a clone with history, Go 1.22 or newer, and tar. From the repository root:

```sh
review_dir=$(mktemp -d)
mkdir "$review_dir/baseline" "$review_dir/candidate"
git archive a1e04d86ace895789b1544276bd84a753a1dfdcf | tar -x -C "$review_dir/baseline"
git archive e6545642902ff429702590190bd37d26f176a789 | tar -x -C "$review_dir/candidate"
cp "$review_dir/candidate/pkg/adapter/scim/scim_test.go" "$review_dir/baseline/pkg/adapter/scim/scim_test.go"
(cd "$review_dir/baseline" && go test ./pkg/adapter/scim -run TestApplyRejectsConcurrentTargetChange -count=1 -v)
(cd "$review_dir/candidate" && go test ./pkg/adapter/scim -run 'TestApplyRejectsConcurrentTargetChange|TestApplyUsesVersionForSuccessfulUpdate' -count=1 -v)
(cd "$review_dir/candidate" && go test ./...)
```

The baseline race test should fail in disable and delete, each reporting one applied mutation and no error. The candidate should return the provider's HTTP 412 without a mutation or automatic retry. The successful-update case should apply exactly one write with the matching version. Compilation failure is not reproduction of the race.

Record Go version, commands, output, and source revisions. These fixtures simulate a provider; they do not test live-provider compatibility.

## Challenge the result

Add a case of your own. Useful questions:

- What if a provider supplies only an individual-resource ETag and no meta.version in search results?
- What if the target version still matches, but the source-derived desired state has become stale?
- What if another account appears between an absence lookup and POST?
- What if the provider ignores If-Match?

Current limits: unversioned writes remain unconditional; feature discovery and individual-resource ETag fallback are absent; POST still depends on provider uniqueness; cursor pagination is unsupported. This implements RFC 7644 section 3.14 rather than introducing a new concurrency mechanism.

An outside review should report what reproduced, what failed, assumptions, and any relevance to a system the reviewer knows. No favorable finding, endorsement, letter, or adoption is expected.
