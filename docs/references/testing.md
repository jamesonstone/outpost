# Testing reference

`make build` compiles `tmp/outpost`. `make test` runs native Go tests with the
race detector. `make check` checks formatting, vet, race tests and Kit. `make fmt`
formats sources; `make clean` removes only the generated binary.

`make integration` requires `OUTPOST_TEST_DSN` pointing to an isolated PostgreSQL
database on an explicit loopback host. It runs all tests including real source
queries, read-only enforcement and the PostgreSQL-to-bbolt-to-HTTP path. Tests
create uniquely named synthetic schemas and remove only their schemas. Never
set this variable to a real application database. PostgreSQL 14 is validated
locally; PR CI provisions a disposable PostgreSQL 16 service.

Without that variable, integration tests explicitly SKIP; native tests continue.
CI runs vet, formatting, all race and integration tests, compilation and a
container build on every PR. HTTP delivery tests use httptest and a synthetic
token; no actual Slack calls occur. Generated binaries and state stay in ignored
tmp paths. No hosted release, deployment or production test is configured.

Manual local smoke: validate `examples/outpost.json`, use an isolated synthetic
source, start with dispatch disabled, inspect loopback health and metrics, stop,
then inspect or back up the state file. CLI inspection requires stopping the
service because bbolt enforces a single process file lock.

Production validation is UNOBSERVED and held. It requires the applicable SOP,
approved read access, persistent-volume and backup owner, verified source
classification/retention/query budget, workspace/channel and credential approval,
and a chosen future activation cutoff. Do not substitute these synthetic tests
for deployed coverage or delivery evidence.
