# aiproxer-cursor-sdk

Standalone [Go-LIP](https://github.com/matdev83/go-llm-interactive-proxy) backend plugin that integrates the
Cursor SDK.

This repository is the Cursor-specific product project. It owns the Cursor connector, the Cursor SDK
bridge, Cursor instrumentation, fixtures, tests, operator documentation, and release tooling, so that
Cursor's JavaScript toolchain and dependencies evolve and release independently of the Go-LIP host.

The standard Go-LIP distribution does not depend on this repository. Cursor support is optional: an
operator installs a released plugin archive through Go-LIP's existing trusted plugin mechanism, and the
Go-LIP host binary is unchanged.

Full host decoupling is the intended end state, not the current one. Once the relocation tracked by the
Go-LIP `cursor-sdk-standalone` specification completes, Node and npm will be required to build and test the
Cursor bridge only inside this repository, and the Go-LIP host will carry no Cursor SDK source and no Node or
npm tooling. Relocation is still in progress: today the Go-LIP host repository still carries the Cursor SDK
source under `connectors/cursorsdk/` and verifies its bridge with its own Node 22.22.3 `npm ci`/`npm test`
lane in
[`.github/workflows/cursor-sdk-platform.yml`](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/.github/workflows/cursor-sdk-platform.yml).
That host-side Node/npm tooling is removed when the bridge and its npm manifests land here.

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