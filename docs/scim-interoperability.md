# SCIM interoperability

## Independent implementation

The suite runs our Go adapter over HTTP against the unchanged PyPI release
`scim2-server==0.8.0`, maintained by the
[python-scim project](https://github.com/python-scim/scim2-server). It uses the
server's own command-line application, filtering, resource storage, PATCH
processing, and version enforcement. Our test code does not implement those
server rules. This complements the adapter's local HTTP fixtures.

All Python dependencies are pinned in `scripts/scim-interop-requirements.txt`.
The CI environment uses Python 3.12 and the Go version declared in go.mod.
The server runs on loopback with a randomly generated test bearer token and an
in-memory backend. Only synthetic identities are created, and cleanup deletes
those identities. The runner terminates the server after the test process exits.

## Reproduce

With Go on PATH, run from the repository root:

```sh
python3 -m venv .venv-scim-interop
. .venv-scim-interop/bin/activate
python -m pip install -r scripts/scim-interop-requirements.txt
python scripts/test-scim-interop.py
```

The runner chooses a local port, starts the installed server, waits for its
discovery endpoint, and executes `go test -race` for the interoperability tests.
All three tests must pass. Ordinary `go test ./...` skips them when
`IDENTITY_SCIM_TEST_URL` is absent; a skip is not interoperability validation.

## Assertions

| Test | Behavior checked |
| --- | --- |
| Lifecycle | Create, replay creation, update displayName, disable, replay disable with a new adapter, rehire, delete, and replay deletion. Each stage checks observed state and cumulative write count. Eight stages result in five writes. |
| Lost disable response | The independent server commits the disable. A client transport wrapper discards its successful response and returns an I/O error. Two attempts through fresh adapters observe the disabled resource and issue no further PATCH. Exactly one disable write reaches the server. |
| Concurrent target change | After our adapter reads a version, a second writer changes displayName on the server. The original conditional PATCH must receive HTTP 412 exactly once, and the second writer's value must remain intact. |

## What this establishes

The current adapter interoperates with this implementation for the tested
scalar User attributes and lifecycle operations. These tests required no
production-code change after the earlier SCIM disable-recovery correction.

The server's backend is in memory. The response-loss test injects a client-side
transport failure after the server responds, not a TCP partition or server
crash. These tests do not cover Groups, complex/multivalued attributes, cursor
pagination, provider-specific schemas, or providers without meta.version.

Passing does not establish full RFC conformance, production certification,
compatibility with Entra/Okta/authentik, evaluation by the python-scim maintainers,
or use of our reconciler by another organization. No such outside evaluation or
adoption is claimed.
