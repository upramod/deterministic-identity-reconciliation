.PHONY: format format-check test vet check example

format:
	find . -name '*.go' -print0 | xargs -0 gofmt -w

format-check:
	test -z "$$(gofmt -l .)"

test:
	go test ./...

vet:
	go vet ./...

check: format-check vet test

example:
	go run ./cmd/reconcile -input examples/events.json -now 2026-09-12T00:00:00Z
