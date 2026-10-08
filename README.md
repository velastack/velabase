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

## Settings from the environment

velabase reads a fixed set of core PocketBase settings from environment variables. A set variable overrides the stored value on every settings load, and the admin UI rejects edits to that field with an inline "Set by the ... environment variable." error. A variable that is set but empty still counts as set.

| Variable | Setting |
|---|---|
| `APP_NAME` | Application name |
| `APP_URL`, else `ORIGIN` | Application URL (`ORIGIN` is what the vela deploy writes; an explicit `APP_URL` wins) |
| `PB_SENDER_NAME`, `PB_SENDER_ADDRESS` | Mail sender name and address |
| `PB_SMTP_HOST`, `PB_SMTP_PORT`, `PB_SMTP_USERNAME`, `PB_SMTP_PASSWORD`, `PB_SMTP_TLS` | SMTP server; setting `PB_SMTP_HOST` enables SMTP |
| `PB_S3_ENDPOINT`, `PB_S3_BUCKET`, `PB_S3_REGION`, `PB_S3_ACCESS_KEY`, `PB_S3_SECRET`, `PB_S3_FORCE_PATH_STYLE` | S3 file storage; setting `PB_S3_ENDPOINT` enables it |
| `PB_BACKUPS_S3_ENDPOINT`, `PB_BACKUPS_S3_BUCKET`, `PB_BACKUPS_S3_REGION`, `PB_BACKUPS_S3_ACCESS_KEY`, `PB_BACKUPS_S3_SECRET`, `PB_BACKUPS_S3_FORCE_PATH_STYLE` | S3 backups storage; setting `PB_BACKUPS_S3_ENDPOINT` enables it |

Booleans accept `true`, `1` or `yes` (case-insensitive). The mail and storage variables are `PB_`-prefixed on purpose: a SvelteKit app served next to velabase loads the same env files, and its own `SMTP_HOST` or `S3_BUCKET` must not switch PocketBase mail or storage on.

## Download

Prebuilt binaries are on the [releases](https://github.com/velastack/velabase/releases) page. The archives use the same `pocketbase_<version>_<os>_<arch>.zip` names as upstream.

## Development

```sh
go run . serve
bash scripts/check.sh          # what CI runs
bash scripts/sync-upstream.sh  # what the sync workflow runs (commits + tags locally, no push)
```
