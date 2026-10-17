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
- RadarModel Schema (`schema/` + `cmd/radarmodel-schema`): not started.

Main promise: never miss a change to a watched model, never cry wolf. Any
provider or network fault is a failed scan (error, exit 2, stored snapshot
untouched), never a removal. `ai/failure_test.go` is the guard; keep every
case passing and add one for each new way a list can come back wrong.

## Agreed plan (October 17, 2026)

1. Core + RadarModel AI, release v0.1.0.  <- in progress
2. Site on the standard Launch template: about, contact
   (info@radarmodel.com), quick start, download, reference; posts for the
   launch, the explainer ("models drifting under your app"), how it was
   tested, the name. Add RadarModel to softdevstudio.com.
3. RadarModel Schema, v0.2.0: Postgres, MySQL, SQLite; baseline mode with no
   migration tool; ignore rules; failure tests (manual ALTER, dropped index and
   type change caught; lost connection or missing permissions not reported as
   drift; large schema); real databases in CI.
4. Left out of v1 on purpose: hosted feed and monitoring, API shape radar,
   scanning code for model strings.

## Working here

- Go version: whatever `go.mod` says. Standard library only; build and test
  with `CGO_ENABLED=0`.
- `go test ./...`, `go test -race ./...`, `go vet ./...`, `gofmt -l .`,
  `sh scripts/smoke.sh ./radarmodel-ai` after `go build ./cmd/radarmodel-ai`.
- Test results are recorded in `test/results` by `scripts/record-tests.sh`,
  or by starting the `record` workflow
  (`gh api -X POST repos/radarmodel/radarmodel/actions/workflows/record.yml/dispatches -f ref=main`).
  Every number on the site comes from there.
- Never claim a test passed without a run that shows it.
