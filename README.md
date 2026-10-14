```text
  ___  _   _ _____ ____   ___  ____ _____
 / _ \| | | |_   _|  _ \ / _ \/ ___|_   _|
| | | | | | | | | | |_) | | | \___ \ | |
| |_| | |_| | | | |  __/| |_| |___) || |
 \___/ \___/  |_| |_|    \___/|____/ |_|
```

**Durable alerts from committed events.**

<!-- BEGIN KIT-MANAGED README BADGES -->
[![Last commit](https://img.shields.io/github/last-commit/jamesonstone/outpost)](https://github.com/jamesonstone/outpost/commits) [![Open issues](https://img.shields.io/github/issues/jamesonstone/outpost)](https://github.com/jamesonstone/outpost/issues) [![Pull requests](https://img.shields.io/github/issues-pr/jamesonstone/outpost)](https://github.com/jamesonstone/outpost/pulls) [![CI](https://github.com/jamesonstone/outpost/actions/workflows/ci.yml/badge.svg)](https://github.com/jamesonstone/outpost/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/jamesonstone/outpost)](https://github.com/jamesonstone/outpost/releases)
<!-- END KIT-MANAGED README BADGES -->

Outpost reads committed placement events from PostgreSQL, keeps delivery state in
an embedded bbolt database, and posts alerts to Slack. It recovers after restarts
and retries failed sends. Dispatch starts disabled.

## Install

Requires Go 1.23 or later, Git, and Make.

```sh
git clone https://github.com/jamesonstone/outpost.git
cd outpost
make build
```

The executable is `tmp/outpost`. You can also build a container with
`docker build -t outpost .`.

## Configure

```sh
cp examples/outpost.json outpost.json
```

Edit `outpost.json` before starting:

- `state_path`: a file in an existing, owner-only directory on persistent storage.
- `source_id`: a stable name identifying your source database and environment.
- `query_file`: your read-only SQL projection; follow the [source contract](docs/references/operations.md#source-projection).
- `partner`: the partner to include; `channel`: the Slack channel ID.
- `activation`: the UTC time from which alerts become eligible.
- Keep `dispatch_enabled` set to `false` while checking the source.
- For best-effort state recovery, set `new_state_policy: "from_startup"` to skip
  historical events absent from the ledger after each service start.
- For a private audience, set `workspace_id` and `allowed_user_ids` to enforce
  the exact workspace and channel members before every send (`private_allowlist`).
- To opt into one public workspace channel, set `audience_policy` to
  `workspace_public` and set `workspace_id`. Omit `allowed_user_ids`. Workspace
  members can discover, join, and read that channel. See [audience policies](docs/references/operations.md#slack-audience-policies).

The examples contain synthetic data. Replace their query and future activation
date for your source. Set `OUTPOST_POSTGRES_DSN` through your runtime environment;
remote PostgreSQL connections require `sslmode=verify-full`.

```sh
./tmp/outpost -config outpost.json -command validate
./tmp/outpost -config outpost.json
```

`validate` checks local configuration without connecting. Startup checks the query
against PostgreSQL. Health and metrics are available at `127.0.0.1:8080`:

```sh
curl http://127.0.0.1:8080/readyz
curl http://127.0.0.1:8080/metrics
```

To send alerts, provide `OUTPOST_SLACK_TOKEN`, invite its bot to the configured
channel with `chat:write` permission, and restart with `dispatch_enabled: true`
after the source, message, channel and activation cutoff have been approved.

## Inspect and back up

Stop the service before opening its state from another command:

```sh
./tmp/outpost -config outpost.json -command inspect
./tmp/outpost -config outpost.json -command backup -backup /path/to/new-backup.db
```

Run one instance per state file. Preserve the file across restarts. Retries can
produce duplicates if Slack accepted a message but its receipt was lost. The
source must retain events long enough for downtime recovery.

See [operations](docs/references/operations.md) for retries, adapter contracts,
monitoring and recovery. Run `make test` for native tests; see the
[testing guide](docs/references/testing.md) for PostgreSQL integration checks.

The license remains undecided. Keep deployment-specific queries, configuration
and credentials outside this repository.

## Maintainers

Maintained by the [jamesonstone](https://github.com/jamesonstone) team.
