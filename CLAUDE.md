# Notes for Claude

This is the one file that says where the next session starts. Keep it short and
current. What comes next is agreed with the owner before it goes in here.

## How this project runs

RadarModel runs on the owner's low-maintenance workflow: Apache 2.0, CI on
every push to main, releases published automatically, a website at
radarmodel.com (site repo photozeitgeist/radarmodel.com). The owner's part is
the go-ahead, the GitHub org and repos, and granting Claude push access.
Claude does everything else.

Releases are automated. To release, raise `Version` in `doc.go` and push to
`main`; never ask the owner to publish one. [RELEASING.md](RELEASING.md) has
the whole procedure and the checks to run afterwards.

Domain sale or domain value never appears in anything in this repo or on the
site.

## The product

Two products under one roof, on one shared engine, in this one repo:

- `core/`: snapshot, diff, file store, report, alert outputs (webhook, Slack,
  email), HTTP fetcher with retries. Knows nothing about AI or databases.
- `ai/` + `cmd/radarmodel-ai`: RadarModel AI. OpenAI, Anthropic, Google and
  OpenRouter model lists; watch list; severity rules in `ai/classify.go`.
- `schema/` + `cmd/radarmodel-schema`: RadarModel Schema. Postgres
  (pg_catalog), MySQL and MariaDB (information_schema), SQLite (read-only);
  severity rules in `schema/classify.go`. Only this command links the
  database drivers (lib/pq, go-sql-driver/mysql, ncruces/go-sqlite3).

Main promise: never miss a change to a watched model, never cry wolf. Any
provider or network fault is a failed scan (error, exit 2, stored snapshot
untouched), never a removal. A watched model's removal is only breaking once
a second scan confirms it (`confirm_removals`, pending mark in the stored
snapshot), for lists that come back short with no visible error.
`ai/failure_test.go` is the guard; keep every case passing and add one for
each new way a list can come back wrong. For Schema the same promise holds:
a lost connection, revoked privilege, session setting or migration running
mid-scan is a failed scan, never drift, and no password ever shows in an
error. `schema/failure_test.go` and `schema/hardening_test.go` are the guard.

## Agreed plan (October 17, 2026)

1. Core + RadarModel AI, release v0.1.0.  <- done (see below)
2. Site on the standard Launch template (done, see below): about, contact
   (info@radarmodel.com), quick start, download, reference; posts for the
   launch, the explainer ("models drifting under your app"), how it was
   tested, the name. Add RadarModel to softdevstudio.com.
3. RadarModel Schema, v0.2.0: Postgres, MySQL, SQLite; baseline mode with no
   migration tool; ignore rules; failure tests (manual ALTER, dropped index and
   type change caught; lost connection or missing permissions not reported as
   drift; large schema); real databases in CI.  <- done (see below)
4. Left out of v1 on purpose: hosted feed and monitoring, API shape radar,
   scanning code for model strings.

## Where it stands (October 17, 2026)

- **v0.2.0 is out** (RadarModel Schema), published by the release workflow
  from commit f8c49bb. Checked: the Linux archive matches SHA256SUMS, both
  binaries print 0.2.0 and pass `scripts/smoke.sh` and
  `scripts/smoke-schema.sh`.
- Two independent reviews of Schema; every blocker and should-fix from the
  second is fixed in 35ca818 (DSN redaction, Postgres session settings,
  MySQL SELECT and TRIGGER, same-name constraints, SQLite foreign keys and
  virtual tables, server version, wider CI).
- CI runs SQLite on Linux, macOS and Windows and Postgres 12, 16, 17,
  MySQL 8.0, 8.4, MariaDB 10.6, 11.4. test/results was recorded for
  f8c49bb (Postgres 16 and MySQL 8.4).
- v0.1.0 and v0.1.1 (RadarModel AI) came before, October 17.
- **The site** (photozeitgeist/radarmodel.com, standard Launch template,
  content and config only) covers both products. softdevstudio.com lists
  RadarModel as the twelfth project, with notes on 0.1.0 and 0.2.0.
- Next: nothing agreed. Item 4 of the plan stays out of v1. Ask the owner.

## Working here

- Go version: whatever `go.mod` says. Standard library only, apart from the
  three database drivers; build and test with `CGO_ENABLED=0` (the race
  detector needs `CGO_ENABLED=1`). go.sum comes from the `deps` workflow
  (`go mod tidy` on GitHub), since cloud sessions can't reach the Go proxy;
  a local, gitignored go.work can point vanity module paths at clones.
- Schema tests need servers for Postgres and MySQL: set
  `RADARMODEL_TEST_POSTGRES` and `RADARMODEL_TEST_MYSQL` (see
  `schema/dialect_test.go`); `RADARMODEL_TEST_LARGE` sizes the large test.
- `go test ./...`, `CGO_ENABLED=1 go test -race ./...`, `go vet ./...`, `gofmt -l .`,
  `sh scripts/smoke.sh ./radarmodel-ai` and `sh scripts/smoke-schema.sh
  ./radarmodel-schema` after building both commands.
- Test results are recorded in `test/results` by `scripts/record-tests.sh`,
  or by starting the `record` workflow
  (`gh api -X POST repos/radarmodel/radarmodel/actions/workflows/record.yml/dispatches -f ref=main`).
  Every number on the site comes from there.
- Never claim a test passed without a run that shows it.
