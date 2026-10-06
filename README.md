# velabase

[PocketBase](https://pocketbase.io) with the VelaStack plugins built in:

- [pocketbase-openworkflow](https://github.com/velastack/pocketbase-openworkflow): an OpenWorkflow-compatible durable workflow engine with a **Workflows** superuser UI.
- [pocketbase-whatsapp](https://github.com/velastack/pocketbase-whatsapp): WhatsApp one-time code auth.

velabase is a regular Go program that depends on PocketBase and the plugins. It does not patch PocketBase. `main.go` is upstream's [`examples/base/main.go`](https://github.com/pocketbase/pocketbase/blob/master/examples/base/main.go) plus the plugin register calls, and `pocketbase update` points at this repo's releases.

## Versions

**velabase vX.Y.Z is PocketBase vX.Y.Z** plus the latest plugin releases at that time. Each release's notes list the plugin versions.

The [sync upstream](.github/workflows/sync.yaml) workflow checks for a new PocketBase release every 6 hours. When it finds one, it:

1. bumps PocketBase and the plugins (`go get`);
2. merges upstream's `examples/base/main.go` changes into `main.go`;
3. runs [`scripts/check.sh`](scripts/check.sh): vet, build, the plugins' test suites against the new PocketBase, and a smoke test of the binary;
4. commits, tags and publishes the release.

If any step fails, nothing is pushed and the workflow run fails. Fix the cause, for example by releasing a plugin fix or resolving a `main.go` merge conflict, then re-run it from the Actions tab. You can also pick the target version there.

Plugin releases ship with the next PocketBase release.

## Download

Prebuilt binaries are on the [releases](https://github.com/velastack/velabase/releases) page. The archives use the same `pocketbase_<version>_<os>_<arch>.zip` names as upstream.

## Development

```sh
go run . serve
bash scripts/check.sh          # what CI runs
bash scripts/sync-upstream.sh  # what the sync workflow runs (commits + tags locally, no push)
```
