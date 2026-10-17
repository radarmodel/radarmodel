# RadarModel

RadarModel watches the models your app depends on and tells you when one of
them changes underneath it.

Your app calls `gpt-4o` or `claude-sonnet-4-5` by name. Providers retire
models, rename them, shrink their limits and change their prices, and the
first you hear of it is often a failed request in production. RadarModel AI
reads each provider's model list on a schedule, compares it with the last one
it saw, and warns you about the models you actually use.

RadarModel is two products on one engine:

- **RadarModel AI** (`radarmodel-ai`) watches the model lists of OpenAI,
  Anthropic, Google and OpenRouter.
- **RadarModel Schema** (`radarmodel-schema`) watches Postgres, MySQL and
  SQLite schemas for drift: tables, columns, indexes, constraints, views and
  triggers changed outside your migrations.

Both are single binaries that need nothing else installed, under the Apache
License 2.0. This README covers RadarModel AI first;
[RadarModel Schema](#radarmodel-schema) has its own section below.

## Install

Download the archive for your system from the
[latest release](https://github.com/radarmodel/radarmodel/releases/latest),
unpack it, and put `radarmodel-ai` and `radarmodel-schema` on your PATH:

| System | File |
|---|---|
| Linux, x86-64 | radarmodel_linux_amd64.tar.gz |
| Linux, ARM64 | radarmodel_linux_arm64.tar.gz |
| macOS, Apple silicon | radarmodel_darwin_arm64.tar.gz |
| macOS, Intel | radarmodel_darwin_amd64.tar.gz |
| Windows, x86-64 | radarmodel_windows_amd64.zip |

Or build them with Go:

```sh
go install github.com/radarmodel/radarmodel/cmd/radarmodel-ai@latest
go install github.com/radarmodel/radarmodel/cmd/radarmodel-schema@latest
```

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

## RadarModel Schema

Someone runs an `ALTER TABLE` by hand during an incident. An ORM auto-migrates
in one environment. A hotfix drops an index. The database no longer matches
what the code and the migrations say, and the first sign is a failed deploy
or a slow query weeks later. `radarmodel-schema` reads the live schema,
compares it with the last scan or an accepted baseline, and reports the
drift. It needs no migration tool: point it at a database and it records
what's there.

### Quick start

```sh
radarmodel-schema init          # writes radarmodel-schema.json
export PROD_DATABASE_URL='postgres://radar@db.example.com/app?sslmode=require'
radarmodel-schema check         # first run records a baseline
radarmodel-schema check         # later runs report drift
```

```
2026-10-17T19:50:36Z  RadarModel Schema: 2 breaking, 2 warnings
prod  BREAKING  column public.customers.name       nullable yes -> no (inserts without it now fail)
prod  WARNING   column public.customers.phone      new
prod  BREAKING  column public.orders.status        type text -> character varying(30)
prod  WARNING   index public.orders.orders_status  gone
```

### Configuration

`radarmodel-schema.json`, or the file in `$RADARMODEL_SCHEMA_CONFIG`, or
`-config FILE`:

```json
{
  "interval": "1h",
  "timeout": "2m",
  "min_fraction": 0.5,
  "ignore": ["*.tmp_*"],
  "databases": [
    {"name": "prod", "driver": "postgres", "dsn_env": "PROD_DATABASE_URL", "schemas": ["public", "billing"]},
    {"name": "app", "driver": "mysql", "dsn_env": "APP_MYSQL_DSN", "baseline": "pinned"},
    {"name": "local", "driver": "sqlite", "path": "app.db", "ignore": ["main.sessions"]}
  ],
  "alerts": {"min_severity": "warning", "slack": {"url_env": "SLACK_WEBHOOK_URL"}}
}
```

- **databases.** `driver` is `postgres`, `mysql` (MySQL or MariaDB) or
  `sqlite`. Connection strings carry passwords, so they come from the
  environment variable `dsn_env` names: a URL or keyword string for Postgres
  (`lib/pq`), `user:pass@tcp(host:3306)/dbname` for MySQL. They never appear
  in output or alerts; one that can't be parsed is reported as
  `DSN_ENV: invalid connection string`. SQLite takes a `path`, relative to the
  config file.
- **schemas.** Postgres watches every schema but the system ones unless you
  list some, and leaves out objects that belong to an extension (an extension
  upgrade isn't drift in your schema). MySQL watches the connection's
  database unless you list some.
- **ignore.** Globs on qualified names, per database or for all: `*` matches
  anything, dots included. A pattern that matches a table covers everything
  in it. Adding an ignore rule never shows up as objects being dropped.
- **baseline.** `rolling` (the default) compares each scan with the one
  before, so each change is reported once. `pinned` compares every scan with
  an accepted baseline, so drift is reported on every run until
  `radarmodel-schema accept DB` takes the current schema as the new baseline.
- **min_fraction.** In a schema of 10 or more tables and views, a scan
  finding fewer than this share of them is a failed scan, not a mass drop:
  usually a connection string pointing at the wrong database. `0` turns it
  off.
- **timeout** bounds a whole scan of one database. **interval**,
  **state_dir** and **alerts** work as for RadarModel AI.

### What counts as what

| Change | Severity |
|---|---|
| A table, view or column dropped | breaking |
| A column's type changed | breaking |
| A column became NOT NULL | breaking |
| A new table, view, column, index, constraint or trigger | warning |
| An index, constraint or trigger dropped or redefined | warning |
| A column's default, generated expression or collation changed, or it became nullable | warning |
| A view's definition changed | warning |
| A table's storage engine or kind changed | warning |
| A partition attached or detached, row-level security switched | warning |
| A column's position changed | info |
| The server's version changed | info |
| Anything inside a table that was itself added or dropped | info |

### What a failed scan is

Nothing is reported as drift because the connection, the server or the
user's privileges misbehaved. These are failed scans, with the stored
snapshot left as it was:

- a lost connection, an unreachable server, a wrong password, a missing
  `dsn_env` variable, or a scan longer than `timeout`
- a SQLite file that's missing, isn't a database, or is a folder. The file is
  opened read-only, so a wrong path never creates an empty database
- a schema that keeps changing while it's read: every scan reads the schema
  twice and only accepts two identical reads, so a migration running during
  the scan can't produce a half-changed snapshot
- too few tables (`min_fraction`)
- on MySQL, objects hidden by a revoked privilege. MySQL's
  `information_schema` only lists what the user can access, so before any
  table, column or trigger is reported as dropped, RadarModel Schema queries
  it: "doesn't exist" is a drop, "access denied" is a failed scan. If the
  change is real and you'd rather not restore the privilege,
  `radarmodel-schema accept DB` takes the schema as it is now

Postgres is read from `pg_catalog`, which lists every object whatever the
user's privileges, so revoking a GRANT there changes nothing at all. Each
read sets its own `search_path`, `IntervalStyle` and `DateStyle`, so a role
or connection string with other settings doesn't change how definitions
read.

The user needs to read the catalog only. On Postgres, any role that can
connect is enough. On MySQL, give it `SELECT`, `SHOW VIEW` and `TRIGGER` on
what it should watch:

```sql
GRANT SELECT, SHOW VIEW, TRIGGER ON app.* TO 'radar'@'%';
```

Without `SELECT`, a dropped table can't be told apart from a hidden one, so
the first scan checks that every table can be read and fails if one can't.
`TRIGGER` is what lets MySQL show the triggers at all.

A server upgrade can change how types and definitions are printed. The
server's version is stored with each snapshot; when it changes, the report
says so and notes it on every other change found in that scan. MySQL's
integer display widths (`int(11)`), which 8.0.19 stopped printing, are left
out everywhere.

SQLite in WAL mode keeps a `-wal` and a `-shm` file next to the database.
Reading it creates them if they're missing, so the folder has to be
writable, even though the database itself is opened read-only.

### Commands

```
radarmodel-schema init   [-config FILE]                  write a starter config
radarmodel-schema check  [-config FILE] [flags] [DB...]  scan, compare, report
radarmodel-schema watch  [-config FILE] [flags]          scan every interval until stopped
radarmodel-schema accept [-config FILE] DB...            take the current schema as the baseline
radarmodel-schema diff   [-config FILE] DB1 DB2          compare two databases now
radarmodel-schema show   [-config FILE] DB               list the stored objects of a database
radarmodel-schema version
```

`check` takes the same flags and exit codes as `radarmodel-ai check`.
`diff` compares two configured databases right now, such as staging against
production, and exits 0 when they match and 1 when they don't. When each
side uses a single schema with different names (`app_staging` and `app`), the
names are lined up first. Comparing a MySQL database with a MariaDB one
shows differences in how defaults are quoted (MariaDB prints `NULL` for no
default, and quotes string defaults); compare like with like. `watch` alerts about the same drift once, and
again only when it changes.

### What it reads

| | Tables and views | Columns | Indexes | Constraints | Triggers |
|---|---|---|---|---|---|
| Postgres 12 to 17 (`pg_catalog`) | kind, view definition, partition bounds and key, row-level security | type, NOT NULL, default, generated or identity, collation, position | full definition | primary key, unique, foreign key, check, exclusion | definition, enabled or disabled |
| MySQL 8.0 and 8.4, MariaDB 10.6 and later (`information_schema`) | kind, engine, view definition | type, NULL, default, extra and generated expression, collation, position | unique, method, columns with order, expressions, visibility | primary key, unique, foreign key with rules, check, enforcement | timing, event, statement |
| SQLite (`sqlite_master` and pragmas) | kind, CREATE statement | type, NOT NULL, default, generated, position | CREATE statement | foreign keys | CREATE statement |

SQLite keeps CHECK constraints only in the CREATE TABLE statement; a change
there shows as the table's `sql` changing. Its foreign keys have no names, so
they're named after their columns and the table they reference
(`main.orders.fk_customer_id_customers`). A virtual table is recorded by its
CREATE statement only. MariaDB before 10.8 accepts `DESC` in an index and ignores it, so there's
no order to compare. MySQL lets a unique key and a foreign key share a
name; when two constraints on a table do, their type is added to the name
(`app.orders.fk_a (unique)`).

## How it was tested

RadarModel Schema's tests run against real databases: SQLite on Linux, macOS
and Windows, and Postgres 12, 16 and 17, MySQL 8.0 and 8.4, and MariaDB
10.6 and 11.4 in containers on every push. Every kind of manual change above is made for real and has to be
reported at the right severity, once. The failure tests lose the connection
in the middle of a scan, stop the server, use a wrong password, move and
corrupt the SQLite file, run a migration between the two reads, revoke
privileges on Postgres and MySQL (tables and triggers), scan Postgres under
other session settings, and drop most of the tables; none of them may be
reported as drift, and no password may appear in an error. A large-schema test scans 2,000 tables of eight
columns and an index each.

For RadarModel AI, the tests cover the commands, the four providers, the
severity rules and the alert outputs, and the failure tests aim at the main
promise. A fake API
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
The engine in `core` doesn't know about AI models or databases: RadarModel
Schema uses the same snapshot, diff, store and alert code, with objects such
as `column public.users.email` instead of models.

## License

Apache License 2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE) and
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
