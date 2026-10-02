# aiproxer-cursor-sdk

Standalone [Go-LIP](https://github.com/matdev83/go-llm-interactive-proxy) backend plugin that integrates the
Cursor SDK.

This repository is the Cursor-specific product project. It owns the Cursor connector, the Cursor SDK
bridge, Cursor instrumentation, fixtures, tests, operator documentation, and release tooling, so that
Cursor's JavaScript toolchain and dependencies evolve and release independently of the Go-LIP host.

The standard Go-LIP distribution does not depend on this repository. Cursor support is optional: an
operator installs a released plugin archive through Go-LIP's existing trusted plugin mechanism, and the
Go-LIP host binary is unchanged.

Full host decoupling is the intended end state, not the current one. Node and npm are now required to
build and test the Cursor bridge only inside this repository, and this repository verifies its own bridge
on Node 22.22.3 with `npm ci`, `npm test`, and `npm run typecheck`. Relocation is still in progress: the
Go-LIP host repository still carries its own copy of the Cursor SDK source under `connectors/cursorsdk/`
and still verifies its bridge with its own Node 22.22.3 `npm ci`/`npm test` lane in
[`.github/workflows/cursor-sdk-platform.yml`](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/.github/workflows/cursor-sdk-platform.yml).
That host-side copy and its Node/npm tooling are removed when the cutover completes.

## Status

Relocation is in progress. The Go connector lives here: `cmd/lip-backend-cursorsdk/`,
`internal/service/`, `internal/product/` (provider adapter, lifecycle, diagnostics, protocol, fixtures,
and the Go test suite), the root-level `service_test.go` and `manifest_posture_parity_test.go`, plus
`release.yaml`, `manifest/template.backendplugin.json`, the relocated example configuration
`config/examples/cursor-sdk-experimental.yaml`, and the relocated live-bridge harness scripts
`scripts/test-cursor-sdk-live-bridge.ps1` and `scripts/test-cursor-sdk-live-bridge.sh`.

The SDK bridge now lives here as well, under [`bridge-node/`](bridge-node), with its source, production
lockfile, SDK fixtures/tests, and Node tooling: `@cursor/sdk` pinned to exact `1.0.23`, the `undici`
`6.28.1` security override, `engines.node >=22.13`, and a CI lane pinned to Node `22.22.3`. Its 108 hermetic
bridge tests pass unchanged from this repository, and the shared Go/TypeScript fixtures it reads from
`internal/product/testdata/fixtures` were relocated with the connector. The Go-LIP host still carries its
own copy of the bridge until the cutover completes. Do not install anything from this repository: no
plugin release has been published, and no private runtime packaging exists yet.

## Default bridge resolution

With no `bridge_executable` in the plugin configuration, the connector resolves its private
companion launcher as a direct path relative to the running outer plugin executable:
`../private/bridge/lip-cursor-sdk-bridge[.exe]` next to `bin/lip-backend-cursorsdk[.exe]`,
matching the release archive layout. Resolution never consults `PATH`, a shell, npm, a global
binary directory, or the current working directory, and it never installs or downloads
anything. If that file is absent, configuration fails with an explicit prerequisite error that
names the expected relative location; there is no fallback to another Cursor integration.

An explicit `bridge_executable` keeps its previous meaning: a direct bridge binary given as an
absolute path or as a `PATH` name, with the same rejection of shell and npm launchers. Set it
explicitly when running from a source checkout, for example
`bridge_executable: lip-cursor-sdk-bridge` resolved through `PATH` after building the bridge in
`bridge-node/`.

The packaged private companion launcher does not exist yet: `cmd/lip-cursor-sdk-bridge` and the
archive assembly that stages the private Node runtime are later work. Until an installed plugin
package provides that file, the packaged default has nothing to resolve and a source checkout
needs an explicit `bridge_executable`.

## Pinned host contract baseline

The module depends only on published Go-LIP modules. There are no `replace` directives, no `go.work`
file, and no sibling checkout requirement.

| Module | Pinned version |
| --- | --- |
| `github.com/matdev83/go-llm-interactive-proxy` | `v0.1.0-rc.1` |
| `github.com/matdev83/go-llm-interactive-proxy/connector-support/acp` | `v0.1.0-rc.1` |

The relocated connector imports only the public contract packages of those modules — `pkg/lipapi`,
`pkg/lipsdk`, `pkg/lipsdk/backendplugin`, `pkg/lipsdk/modelinventory`, `api/backendplugin/v1`, and
`connector-support/acp` — so `go build ./...` and `go test ./...` prove the pins resolve from a clean
checkout. The connector never imports host `internal/` packages, and no generated protobuf code is
vendored here: `api/backendplugin/v1` comes from the released root module.

## Verifying

The Go checks require only a Go toolchain; Node and npm are not needed for them.

```sh
GOWORK=off go mod download
GOWORK=off go mod verify
GOWORK=off go mod tidy -diff
GOWORK=off go build ./...
GOWORK=off go test ./...
```

The bridge checks require Node `>=22.13` (CI pins `22.22.3`) and run in `bridge-node/`:

```sh
cd bridge-node
npm ci
npm test
npm run typecheck
```

Both lanes run in CI. SDK and JavaScript dependency maintenance, including the pinned `@cursor/sdk`
version and the `undici` security override, belongs here and never to the Go-LIP host.

## Documentation

- Plugin authoring and the executable backend-plugin ABI: Go-LIP
  [`docs/backend-plugins/authoring.md`](https://github.com/matdev83/go-llm-interactive-proxy/blob/main/docs/backend-plugins/authoring.md).
- Go-LIP host repository: [`matdev83/go-llm-interactive-proxy`](https://github.com/matdev83/go-llm-interactive-proxy).

## License

MIT. See [LICENSE](LICENSE). Extraction provenance and attribution of host-derived code are recorded in
[PROVENANCE.md](PROVENANCE.md).