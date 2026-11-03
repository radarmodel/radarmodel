# Checking `radarmodel-ai scan` by hand on three AI agents

`radarmodel-ai scan` should find every model ID a codebase calls, and nothing
that isn't one. It was run on three open source AI agents at pinned commits
(`scripts/scan-case-studies.sh`, output in [scan-agents.txt](scan-agents.txt)),
and the results were checked by hand, both ways:

- **Found:** every model the scan reports was looked up at the lines it
  names, to see that it is a model ID and that the line uses it.
- **Missed:** a script listed every model-like word on the repo's
  non-comment lines (any `gpt-`, `o1` to `o9`, `claude-`, `gemini-`,
  `gemma-`, `text-embedding-` word, alone or after a `vendor/` prefix) that
  the scan didn't report at that line. Each was read in context.

| Repo | Commit | Files read | Models found | Lines |
|---|---|---|---|---|
| [Aider-AI/aider](https://github.com/Aider-AI/aider) | 5dc9490 | 228 | 189 | 714 |
| [assafelovic/gpt-researcher](https://github.com/assafelovic/gpt-researcher) | 3582a68 | 528 | 53 | 196 |
| [browser-use/browser-use](https://github.com/browser-use/browser-use) | b49a68e | 445 | 184 | 435 |

## Found

Every reported model is a model ID used where the scan says: in model tables
(aider's `model-settings.yml`, browser-use's `llm/models.py`), defaults,
config files, `.env` examples, tests, and the agents' own lists of what they
support. No prose, comment, URL or file name was reported.

One finding is a real bug: browser-use's default model in one place is
`gpt-o4-mini`, which no provider lists. RadarModel reports it as unlisted
once a snapshot of OpenAI's list is stored, which is what it is for.

After the review, the scan also leaves out strings in comments after code,
file names, `package.json`, bare `o2` and `o4` to `o9` (only `o1` and `o3`
exist on their own), and a config value read twice. On these repos that
dropped browser-use's `'o4'` (an entry in a list of reasoning-model
prefixes) and gpt-researcher's five `package.json` files, which held no
model, and removed doubled locations.

Accepted on purpose: a list of model-name prefixes the code loops over
(`for pattern in ["gpt-4o", ...]`) is reported, because a list is how code
names the models it supports, and RadarModel can't tell the loop apart.

## Missed, and why

None of the words left over is a model the code calls by that name. They
fall into these groups:

- **Substring and prefix tests.** `"claude-opus-4-7" in self.name`,
  `model.startswith("gpt-4.1")`, `'o3' in model_name`: the code checks a
  name, it doesn't call one. The scan skips the string before `in`, and
  `startswith`, `includes`, `contains` and the like.
- **Prose.** Docstrings, print and log messages, error texts ("pass a model
  such as 'openai/gpt-5.5'"), issue templates' descriptions, benchmark write-ups.
- **Recorded results.** aider's leaderboards and gpt-researcher's benchmark
  JSON carry model names as data and as the commands that were run
  (`aider --model gemini/gemini-1.5-flash-002`), not as calls.
- **URLs.** `https://ai.google.dev/gemini-api/docs/pricing#gemini-2.5-flash`.
- **Providers RadarModel AI doesn't read yet.** `azure/gpt-4.1` and 35 more
  Azure names in aider's settings. They come with Azure OpenAI support in
  the next release.
- **Test placeholders.** `github/o1-mini`, `something/o3-mini`: made-up
  prefixes in aider's tests.
- **Fragments.** `gpt-4-`, `gpt-oss` (no version), a `replace()` target
  built from another name.

Counts of the leftover words, by kind: aider 58 bare, 36 `azure/`, 19
`gemini/` (all in leaderboard command lines), 5 `openai/`, 4 `something/`,
3 `anthropic/`, 2 `github/`; gpt-researcher 41 bare, 2 `o3/`, 2 `models/`;
browser-use 24 bare and one each of `openai/`, `google/`, `anthropic/`.
Every one was read, and every one is in a group above.
