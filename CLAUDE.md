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
untouched), never a removal. A watched model's removal is only breaking once
a second scan confirms it (`confirm_removals`, pending mark in the stored
snapshot), for lists that come back short with no visible error.
`ai/failure_test.go` is the guard; keep every case passing and add one for
each new way a list can come back wrong.

## Agreed plan (October 17, 2026)

1. Core + RadarModel AI, release v0.1.0.  <- done (see below)
2. Site on the standard Launch template (done, see below): about, contact
   (info@radarmodel.com), quick start, download, reference; posts for the
   launch, the explainer ("models drifting under your app"), how it was
   tested, the name. Add RadarModel to softdevstudio.com.
3. RadarModel Schema, v0.2.0: Postgres, MySQL, SQLite; baseline mode with no
   migration tool; ignore rules; failure tests (manual ALTER, dropped index and
   type change caught; lost connection or missing permissions not reported as
   drift; large schema); real databases in CI.
4. Left out of v1 on purpose: hosted feed and monitoring, API shape radar,
   scanning code for model strings.

## Where it stands (October 17, 2026)

- **v0.1.0 is out**, published by the release workflow from commit fa32afb
  with five archives and SHA256SUMS. Checked: the Linux archive matches its
  checksum, prints `radarmodel-ai 0.1.0` and passes `scripts/smoke.sh`.
- An independent review found four blockers and six should-fixes; all are
  fixed in 2f4e8ed (second-scan confirmation of removals, name collisions,
  cross-host redirects, webhook URLs in errors, Anthropic limits and
  lifecycle, OpenRouter paging, store lock, and more).
- test/results was recorded on GitHub: every test, race detector, failure
  tests three times, smoke test, and a live scan of OpenRouter's public list
  (470 models parsed, second scan no changes).
- **v0.1.1** followed the same day: the headline says "2 warnings", not
  "2 warning". Checked the same way as 0.1.0.
- **The site is built** at photozeitgeist/radarmodel.com on the standard
  Launch template, unchanged (content and config values only; the repo came
  as a copy of the AltSql site and all of its content was replaced). Pages:
  about, contact, quick start, download, reference. Posts: the explainer,
  the launch, how it was tested, the name. Four images drawn into
  static/images. The quick start's output was taken from the released binary
  run on saved lists. Built with Hugo, every internal link checked, looked at
  in Chromium at 1280 and 390 px.
- **softdevstudio.com** lists RadarModel as the twelfth project, with a note
  on the launch.
- Next: step 3, RadarModel Schema (v0.2.0), when the owner says go.

## Working here

- Go version: whatever `go.mod` says. Standard library only; build and test
  with `CGO_ENABLED=0` (the race detector needs `CGO_ENABLED=1`).
- `go test ./...`, `CGO_ENABLED=1 go test -race ./...`, `go vet ./...`, `gofmt -l .`,
  `sh scripts/smoke.sh ./radarmodel-ai` after `go build ./cmd/radarmodel-ai`.
- Test results are recorded in `test/results` by `scripts/record-tests.sh`,
  or by starting the `record` workflow
  (`gh api -X POST repos/radarmodel/radarmodel/actions/workflows/record.yml/dispatches -f ref=main`).
  Every number on the site comes from there.
- Never claim a test passed without a run that shows it.
