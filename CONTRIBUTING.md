# Contributing

Contributions should improve deterministic behavior, testability, security, or
interoperability.

## Before opening a pull request

Run:

```sh
gofmt -w $(find . -name '*.go')
go vet ./...
go test ./...
```

Add a regression test for every behavior change. Keep fixtures synthetic and
free of personal or employer-confidential data.

## Design rules

- Keep the pure reconciliation engine free of network and database calls.
- Do not change freshness ordering without updating the public specification.
- Do not add a provider adapter without a target equivalence test.
- Do not use arrival time as a source ordering field.
- Prefer small, reviewable changes.
