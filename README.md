# outpost
🗼 Durable Alerts From Committed Events.

<!-- BEGIN KIT-MANAGED README BADGES -->
[![Last commit](https://img.shields.io/github/last-commit/jamesonstone/outpost)](https://github.com/jamesonstone/outpost/commits) [![Open issues](https://img.shields.io/github/issues/jamesonstone/outpost)](https://github.com/jamesonstone/outpost/issues) [![Pull requests](https://img.shields.io/github/issues-pr/jamesonstone/outpost)](https://github.com/jamesonstone/outpost/pulls) [![CI](https://github.com/jamesonstone/outpost/actions/workflows/ci.yml/badge.svg)](https://github.com/jamesonstone/outpost/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/jamesonstone/outpost)](https://github.com/jamesonstone/outpost/releases)
<!-- END KIT-MANAGED README BADGES -->

## Service

Outpost is a single-instance Go service that reads committed placement events from
PostgreSQL, records logical notifications and checkpoints in bbolt, and delivers
through Slack. The repository contains generic contracts and synthetic examples.
Deployment-specific queries, mappings, links, credentials and configuration belong
outside this repository.

Dispatch is disabled by default. Source and destination adapters are narrow:
`core.Source.Page` returns a bounded normalized page; `core.Destination.Send`
returns a confirmed receipt or a categorized failure. Other adapters can use the
same durable reconciliation and delivery core.

## Build and validate

```sh
make build
make check
./tmp/outpost -config examples/outpost.json -command validate
# For an isolated, disposable loopback PostgreSQL database only:
OUTPOST_TEST_DSN='postgres://synthetic@127.0.0.1:5432/synthetic?sslmode=disable' make integration
```

Go 1.23 or later is required. See [testing reference](docs/references/testing.md).
The example activation date is intentionally in the future; choose a reviewed
cutoff before using a source. `validate` checks local shape only and makes no
external connections. Startup additionally preflights the projection against
PostgreSQL before creating delivery state.

## Source projection

Supply a trusted SELECT in an operator-owned file with these columns:

| Column | Contract |
| --- | --- |
| `id` | Stable, unique publication identity; immutable timestamp and identity |
| `environment` | Explicit `production` evidence |
| `tenant`, `order_id` | Authoritative ownership and logical placement identity |
| `partner` | Must match the configured partner |
| `placed_at` | Original acceptance occurrence, corroborated by committed placement evidence |
| `link` | Approved HTTPS link requiring authentication; no credentials or sensitive query values |
| `kind` | `first_placement` for the original acceptance |
| `traffic` | Explicit `production` classification |
| `origin` | `partner_api` from authoritative creation provenance |
| `tenant_class` | Explicit `production`; tests and synthetic tenants must map to another value |

`examples/projection.sql` and `examples/events.sql` illustrate this contract using
synthetic data. They are not source migrations. Runtime never installs tables or
indexes. Projection authors must join committed acceptance and authoritative
classification/provenance without reading raw requests or unnecessary data.
Unknown classification fails closed and appears in classification exceptions.
Excluded or pre-cutoff rows produce no delivery attempts.

Every page runs in a read-only transaction with a ten-second query bound. Use a
least-privilege reader with restricted column grants. Remote connections require
verified TLS (`sslmode=verify-full`); explicit plaintext is accepted only for
loopback test servers. No credentials appear in config, logs or stored errors.

## Durable reconciliation and delivery

Eligibility starts at an immutable UTC occurrence cutoff, inclusively. This is
not a commit-time cutoff: source occurrence timestamps may precede commit.
Scanning captures a fixed upper occurrence bound, pages by `(placed_at, id)` and
atomically commits pending notifications with each checkpoint. After a complete
sweep it restarts from the activation boundary. This catches transactions that
commit late behind a prior cursor; a permanently advancing timestamp is unsafe.

A full sweep and subsequent dispatch must fit the intended five-minute latency
budget. Source volume, indexing and retention must be measured for the actual
projection. Retention must exceed the supported outage window. Missing or deleted
source evidence cannot be reconstructed. The growing retained activation range
has an increasing scan cost; this first version does not replace a commit-ordered
feed for large workloads.

Delivery identity combines source contract, environment, tenant, order, placement
kind and pinned channel. Re-publications reuse one logical delivery. Changed
payloads create visible conflicts and stop additional sends; an already active
attempt still retains its receipt or ambiguity. Policy and projection hash bind
the state file: changing the source contract, cutoff, partner or channel fails
closed. Source ID must identify one immutable database/environment; do not reuse
it for another database.

Claims use random tokens and a one-minute lease, exceeding the fifteen-second
request timeout. Slack runs outside transactions. Expired claims and lost or
malformed responses are recorded as ambiguous. Coverage-first retry can produce
a duplicate when Slack accepted a message whose receipt was lost. Exactly-once
external delivery is not promised. Transient retries use capped exponential
backoff with jitter and no attempt cap. Retry-After is honored across the pinned
channel and persists across restart. Permanent failures remain inspectable and
require explicit requeue using the original identity.

Messages contain only order ID, partner, original placement time and approved
link. Slack syntax is escaped and mentions and unfurls are suppressed. Credentials
are read from `OUTPOST_POSTGRES_DSN` and, only for enabled dispatch,
`OUTPOST_SLACK_TOKEN` at runtime. Never commit them or print source response bodies.

## Operations

Create the state directory with owner-only access on a persistent local volume;
bbolt creates the database with mode 0600 and enforces a single-process lock.
Do not use ephemeral storage or run multiple active instances against a file.
Default config: 500 rows/page, 60 seconds between complete sweeps, dispatch off,
loopback health listener `127.0.0.1:8080`. The listener must stay loopback; a
container health check can run inside its network namespace.

```sh
./tmp/outpost -config /private/outpost.json
# Stop the service before opening state from another command:
./tmp/outpost -config /private/outpost.json -command inspect -limit 100
./tmp/outpost -config /private/outpost.json -command attempts -key DELIVERY_KEY
./tmp/outpost -config /private/outpost.json -command retry -key FAILED_DELIVERY_KEY
./tmp/outpost -config /private/outpost.json -command backup -backup /private/new-backup.db
```

Backups use a consistent bbolt read transaction and never overwrite an existing
file. Test restore into a separate state file. Restoring an older snapshot can
cause duplicates for receipts lost after the snapshot. Identity and attempt
records are retained; automatic pruning is not implemented. Inspection is bounded
to the first 1,000 delivery records; attempts are queried by logical key.

`/healthz` reports process liveness. `/readyz` requires a successful complete sweep
within five minutes. `/metrics` exposes dispatch mode, last sweep age, oldest
pending age, state counts, attempts, ambiguity and classification exceptions,
without order identities. Alert on stale sweeps, growing pending age, failed or
conflicting deliveries, exceptions and unavailable storage. Logs contain only
fixed failure categories. Disable dispatch to stop new sends while retaining
reconciliation and pending state; an already active HTTP attempt may complete.

The Dockerfile builds a nonroot container; it does not publish or deploy it.
Production access, credentials, hosting/storage, backups, source coverage and
retention, audience/channel verification, activation cutoff and change-control
approval must be resolved before enabling delivery. No production activation
configuration is supplied. The license remains undecided.

## Maintainers

Maintained by the [jamesonstone](https://github.com/jamesonstone) team.
