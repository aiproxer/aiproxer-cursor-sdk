# aiproxer-cursor-sdk

Standalone [Go-LIP](https://github.com/matdev83/go-llm-interactive-proxy) backend plugin that integrates the
Cursor SDK.

This repository is the Cursor-specific product project. It owns the Cursor connector, the Cursor SDK
bridge, Cursor instrumentation, fixtures, tests, operator documentation, and release tooling, so that
Cursor's JavaScript toolchain and dependencies evolve and release independently of the Go-LIP host.

The standard Go-LIP distribution does not depend on this repository. Cursor support is optional: an
operator installs a released plugin archive through Go-LIP's existing trusted plugin mechanism, and the
Go-LIP host binary is unchanged. Node and npm are required to build and test the Cursor bridge, but only
inside this repository; the Go-LIP host carries no Node or npm tooling.

## Status

This repository is currently a skeleton. It contains the module, license, provenance, and independent
verification workflows only. No Cursor connector source, no SDK bridge, and no npm manifests have been
moved here yet; that relocation is tracked in the Go-LIP `cursor-sdk-standalone` specification. Do not
install anything from this repository: no plugin release has been published.

## Pinned host contract baseline

The module depends only on published Go-LIP modules. There are no `replace` directives, no `go.work`
file, and no sibling checkout requirement.

| Module | Pinned version |
| --- | --- |
| `github.com/matdev83/go-llm-interactive-proxy` | `v0.1.0-rc.1` |
| `github.com/matdev83/go-llm-interactive-proxy/connector-support/acp` | `v0.1.0-rc.1` |

`internal/pinnedcontracts` imports the public contract packages of those modules so that `go build ./...`
and `go test ./...` prove the pins resolve from a clean checkout.

## Verifying

Requires only a Go toolchain. Node and npm are not needed for the Go checks and no npm manifest exists yet.

```sh
GOWORK=off go mod download
GOWORK=off go mod verify
GOWORK=off go mod tidy -diff
GOWORK=off go build ./...
GOWORK=off go test ./...
```

The Node/npm verification lane (SDK bridge install, tests, typecheck) is defined in the workflow but
disabled while no bridge exists; it is enabled by the task that relocates the Cursor SDK bridge and its
npm manifests into this repository. From that point on, SDK and JavaScript dependency maintenance belongs
here and never to the Go-LIP host.

## Documentation

- Plugin authoring and the executable backend-plugin ABI: Go-LIP
  [`docs/backend-plugins/authoring.md`](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/docs/backend-plugins/authoring.md).
- Go-LIP host repository: [`matdev83/go-llm-interactive-proxy`](https://github.com/matdev83/go-llm-interactive-proxy).

## License

MIT. See [LICENSE](LICENSE). Extraction provenance and attribution of host-derived code are recorded in
[PROVENANCE.md](PROVENANCE.md).