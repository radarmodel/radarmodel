# RadarModel

RadarModel watches the models your app depends on and tells you when one of
them changes underneath it.

Your app calls `gpt-4o` or `claude-sonnet-4-5` by name. Providers retire
models, rename them, shrink their limits and change their prices, and the
first you hear of it is often a failed request in production. RadarModel AI
reads each provider's model list on a schedule, compares it with the last one
it saw, and warns you about the models you actually use.

RadarModel is two products on one engine:

- **RadarModel AI** (`radarmodel-ai`, available now) watches the model lists
  of OpenAI, Anthropic, Google and OpenRouter.
- **RadarModel Schema** (coming next) watches database schemas for drift.

Both are single binaries with no dependencies, under the Apache License 2.0.

## Install

Download the archive for your system from the
[latest release](https://github.com/radarmodel/radarmodel/releases/latest),
unpack it, and put `radarmodel-ai` on your PATH:

| System | File |
|---|---|
| Linux, x86-64 | radarmodel_linux_amd64.tar.gz |
| Linux, ARM64 | radarmodel_linux_arm64.tar.gz |
| macOS, Apple silicon | radarmodel_darwin_arm64.tar.gz |
| macOS, Intel | radarmodel_darwin_amd64.tar.gz |
| Windows, x86-64 | radarmodel_windows_amd64.zip |

Or build it with Go: `go install github.com/radarmodel/radarmodel/cmd/radarmodel-ai@latest`

## Quick start

```sh
radarmodel-ai init            # writes radarmodel.json
# edit "watch" to list the models your app calls, then:
export OPENAI_API_KEY=...  ANTHROPIC_API_KEY=...  GEMINI_API_KEY=...
radarmodel-ai check           # first run records a baseline
radarmodel-ai check           # later runs report what changed
```

Run `check` from cron or CI, or leave `radarmodel-ai watch` running to scan on
an interval.

A report looks like this:

```
2026-10-17T09:00:00Z  RadarModel AI: 1 breaking, 1 warning
openai      BREAKING  gpt-4o *                       gone (possibly renamed to gpt-4o-2026-10)
openrouter  WARNING   anthropic/claude-sonnet-4.5 *  input_price 0.000003 -> 0.0000035
anthropic   ok        no changes (9 items)
google      ok        2 minor changes below warning (51 items)
```

A `*` marks a model you watch.

## What counts as what

Only models on your watch list can be breaking or warnings. Everything else
in a provider's list is reported as info, so a provider adding forty models
doesn't page anyone.

| Change to a watched model | Severity |
|---|---|
| Gone from the list | breaking |
| Context or output limit went down | breaking |
| A limit is no longer published | warning |
| Price went up, or a price appeared or vanished | warning |
| Got a retirement date (OpenRouter's `expiration_date`) | warning |
| Lost a supported method (Google) | warning |
| Named in `watch` but not in the list at all | warning, every run |
| Anything else: new models, price cuts, bigger limits, name changes | info |

When a watched model disappears and a new model with the same display name
appears in the same scan, the report says "possibly renamed to ...".

## What a failure looks like

A model list that comes back wrong must never look like models disappearing.
RadarModel AI treats all of these as a failed scan of that provider, reports
the error, and leaves its stored snapshot untouched:

- an HTTP error, after retrying 408, 429 and 5xx up to three times
  (Retry-After is honoured, up to 30 seconds)
- a body that isn't valid JSON, is cut off, or lacks the model list
- an empty list
- a page in the middle of a paginated list failing
- a list holding less than half the models of the last one (`min_fraction`)
- a timeout

The other providers are scanned as usual. Once the provider behaves again, the
next scan compares against the last good snapshot, so nothing is missed.

If a provider really did drop more than half its list, run once with
`"min_fraction": 0` to accept it, then put the setting back.

## Configuration

`radarmodel.json`, or the file in `$RADARMODEL_CONFIG`, or `-config FILE`:

```json
{
  "interval": "1h",
  "timeout": "30s",
  "min_fraction": 0.5,
  "state_dir": ".radarmodel",
  "providers": [
    {"type": "openai"},
    {"type": "anthropic"},
    {"type": "google"},
    {"type": "openrouter"},
    {"type": "openai", "name": "proxy", "base_url": "https://llm-proxy.example.com", "api_key_env": "PROXY_KEY"}
  ],
  "watch": [
    "openai:gpt-4o",
    "anthropic:claude-sonnet-4-5",
    "google:gemini-2.5-flash",
    "openrouter:anthropic/*",
    "proxy:gpt-4o"
  ],
  "alerts": {
    "min_severity": "warning",
    "webhook": {"url_env": "RADAR_WEBHOOK_URL"},
    "slack": {"url_env": "SLACK_WEBHOOK_URL"},
    "email": {"host": "smtp.example.com", "port": 587, "from": "radar@example.com",
              "to": ["ops@example.com"], "username_env": "SMTP_USER", "password_env": "SMTP_PASS"}
  }
}
```

- **providers.** `type` is `openai`, `anthropic`, `google` or `openrouter`.
  `name` defaults to the type; give two entries of one type different names.
  Keys come from `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` and `GEMINI_API_KEY`
  unless `api_key_env` names another variable. OpenRouter's list is public.
  `base_url` points at a proxy or a compatible endpoint; a `file://` URL reads
  saved responses from disk (Linux and macOS).
- **watch.** `source:model`. In the model part, `*` matches anything,
  slashes included. Entries naming a source that isn't configured are
  rejected, so a typo can't leave a model unwatched.
- **alerts.** Sent when a run has something at or above `min_severity`
  (warning by default), or a provider failed. The webhook gets the JSON
  report; Slack and email get a text summary. Secrets are always read from
  environment variables; a variable that is named but not set stops the
  command instead of quietly turning alerts off. Email uses STARTTLS when the
  server offers it (port 587).
- **interval** applies to `watch`, at least one minute. **timeout** applies to
  each HTTP request.
- **state_dir** holds the snapshots, relative to the config file:
  `latest/SOURCE.json` and the last 50 in `history/SOURCE/`. Each file is
  written whole or not at all.

Unknown fields in the config are errors.

## Commands

```
radarmodel-ai init  [-config FILE]           write a starter config
radarmodel-ai check [-config FILE] [flags]   scan once, compare, report
radarmodel-ai watch [-config FILE] [flags]   scan every interval until stopped
radarmodel-ai show  [-config FILE] SOURCE    list the stored models of a source
radarmodel-ai version
```

Flags for `check` and `watch`: `-min info|warning|breaking` sets what the
terminal shows (warning by default), `-json` prints the report as JSON,
`-no-alerts` skips webhook, Slack and email. `check -dry-run` compares but
keeps the stored snapshots.

`check` exits 0 when nothing is breaking, 1 on a breaking change, and 2 when
a provider couldn't be scanned or the config is wrong. Use it as a CI gate.

`watch` reports every scan at the terminal. It sends the "not in the list"
warning for a given model once per run of the command rather than every
interval.

## How it was tested

Every operation has a test, and the failure tests aim at the main promise:
a fake API serving all four providers is made to fail in fourteen ways that
cover every item in the list above, and each test checks that the failure is reported as an error,
that no change is reported, that the stored snapshot is byte-for-byte the
same, and that the next good scan finds nothing to report. The results are
recorded in [test/results](test/results).

## How it works

Every run takes a snapshot of each provider's list: model IDs plus the
properties that matter (display name, limits, prices, retirement date). It
compares the snapshot with the stored one, classifies each difference, sends
the report to the terminal and the alert outputs, and stores the new snapshot.
The engine in `core` doesn't know about AI models; RadarModel Schema will use
the same snapshot, diff, store and alert code for database schemas.

## License

Apache License 2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE) and
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
