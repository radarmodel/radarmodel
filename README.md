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
openai      BREAKING  gpt-4o *                       gone (confirmed by a second scan; possibly renamed to gpt-4o-2026-10)
openrouter  WARNING   anthropic/claude-sonnet-4.5 *  input_price 0.000003 -> 0.0000035
anthropic   ok        no changes (9 items)
google      ok        2 minor changes below warning (51 items)
```

Prices are US dollars per token, as OpenRouter publishes them.

A `*` marks a model you watch.

## What counts as what

Only models on your watch list can be breaking or warnings. Everything else
in a provider's list is reported as info, so a provider adding forty models
doesn't page anyone.

| Change to a watched model | Severity |
|---|---|
| Gone from the list, on two scans in a row | breaking |
| Gone from the list on one scan | warning (see below) |
| Context or output limit went down | breaking |
| Lifecycle became `retired` (Anthropic) | breaking |
| Lifecycle left `active`, such as `deprecated` (Anthropic) | warning |
| Got or changed a retirement date (Anthropic `retires_at`, OpenRouter `expiration_date`) | warning |
| A limit is no longer published | warning |
| Price went up, or a price appeared or vanished | warning |
| Lost a supported method (Google) | warning |
| Named in `watch` but not in the list at all | warning, every run |
| Anything else: new models, price cuts, bigger limits, name changes | info |

What each provider publishes differs. OpenAI's list has model IDs only, so
for OpenAI models RadarModel AI can only tell you that a model is gone.
Anthropic's has limits, lifecycle and retirement dates; Google's has limits
and supported methods; OpenRouter's has limits, prices and retirement dates.

**Removals are confirmed by a second scan.** The first scan that misses a
watched model reports a warning and keeps the model in the stored snapshot;
if the next scan misses it too, that is breaking. A model that is back on the
next scan is reported as info. This keeps a list that came back short without
any visible error (a dropped page, a proxy cutting the body cleanly) from
paging anyone. To report breaking on the first miss, set
`"confirm_removals": false`.

When a watched model disappears and a new model with the same display name
appears in the same scan, the report says "possibly renamed to ...".

## What a failure looks like

A model list that comes back wrong must never look like models disappearing.
RadarModel AI treats all of these as a failed scan of that provider, reports
the error, and leaves its stored snapshot untouched:

- an HTTP error (408, 429 and 5xx get three attempts in all, honouring
  Retry-After up to 30 seconds; other errors aren't retried)
- a body that isn't valid JSON, is cut off, or lacks the model list
- an empty list
- a page in the middle of a paginated list failing, or a paged answer where
  one page was expected (OpenRouter's `links.next` or `total_count`)
- a list holding less than half the models of the last one (`min_fraction`)
- a redirect to another host (refused, so keys never follow it)
- a timeout

The other providers are scanned as usual. Once the provider behaves again, the
next scan compares against the last good snapshot, so nothing is missed. A
list that comes back short with no error at all is what the second-scan
confirmation above is for.

If a provider really did drop more than half its list, run once with
`"min_fraction": 0` to accept it, then put the setting back.

## Configuration

`radarmodel.json`, or the file in `$RADARMODEL_CONFIG`, or `-config FILE`:

```json
{
  "interval": "1h",
  "timeout": "30s",
  "min_fraction": 0.5,
  "confirm_removals": true,
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
  slashes included; a source of `*` matches every provider. Entries naming a
  source that isn't configured are rejected, so a typo can't leave a model
  unwatched. Source names that differ only in case or punctuation are
  rejected too, because they would share a snapshot file.
- **alerts.** Sent when a run has something at or above `min_severity`
  (warning by default), or a provider failed. The webhook gets the JSON
  report; Slack and email get a text summary. Keys, Slack URLs and SMTP
  passwords are read from environment variables named in the config (a
  webhook may also take a plain `url` when it carries no token). A variable
  that is named but not set stops the command instead of quietly turning
  alerts off. Errors name the webhook's host, never its full URL. Email uses
  STARTTLS when the server offers it (port 587).
- **interval** applies to `watch`, at least one minute. **timeout** applies to
  each HTTP request.
- **state_dir** holds the snapshots, relative to the config file:
  `latest/SOURCE.json` and the last 50 in `history/SOURCE/`. Each file is
  written whole or not at all. A `lock` file keeps two runs from using the
  folder at once; one older than 30 minutes is taken to be left over from a
  crash.

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
a provider couldn't be scanned, another run holds the state folder, or the
config is wrong. Breaking wins: a run with a breaking change and a failed
provider exits 1, and the report shows both. Use it as a CI gate.

`watch` reports every scan at the terminal. It sends the "not in the list"
warning for a given model once rather than every interval, and again only if
the model comes back and later goes missing again.

## How it was tested

The tests cover the commands, the four providers, the severity rules and the
alert outputs, and the failure tests aim at the main promise. A fake API
serving all four providers is made to fail in every way listed above, and
each test checks that the failure is reported as an error, that no change is
reported, that the stored snapshot is byte-for-byte the same, and that the
next good scan finds nothing to report. A live scan of OpenRouter's public
list is recorded too. The results are in [test/results](test/results).

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
