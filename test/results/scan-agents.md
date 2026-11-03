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
| [Aider-AI/aider](https://github.com/Aider-AI/aider) | 5dc9490 | 228 | 246 | 875 |
| [assafelovic/gpt-researcher](https://github.com/assafelovic/gpt-researcher) | 3582a68 | 528 | 53 | 195 |
| [browser-use/browser-use](https://github.com/browser-use/browser-use) | b49a68e | 445 | 215 | 472 |

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
model, and removed doubled locations. A second review added block comments
and Python docstrings spanning lines: the usage examples in docstrings
(`ChatVercel(model='openai/gpt-4o')` in a class's description) are prose
now, which removed 13 locations of models the same repos also call in code.
No model dropped out.

Accepted on purpose: a list of model-name prefixes the code loops over
(`for pattern in ["gpt-4o", ...]`) is reported, because a list is how code
names the models it supports, and RadarModel can't tell the loop apart.

## Added in 0.4: Mistral, Groq, xAI, Azure and Bedrock

With the five new providers the scan found 57 more models in aider (36
Azure, 11 Bedrock, 5 xAI, 4 Groq, 1 Mistral) and 31 in browser-use (15 xAI,
12 Mistral, 3 Bedrock, 1 Groq); gpt-researcher uses none of them. Each was
read at its line: aider's model settings (`bedrock/anthropic.claude-...`,
`groq/llama3-70b-8192`, `xai/grok-3-beta`), browser-use's Bedrock and
Mistral clients, its tests, and its list of Vercel AI Gateway names
(`xai/grok-3`, `mistral/devstral-2`), which use the same `vendor/model`
form and are read as that provider's models. A cross-region inference
profile (`us.anthropic.claude-sonnet-4-6`) is reported as the model it
serves, because that's the name Bedrock's model list uses. No new finding
came from prose or a comment.

## Missed, and why

None of the words left over is a model the code calls by that name. They
fall into these groups:

- **Substring and prefix tests.** `"claude-opus-4-7" in self.name`,
  `model.startswith("gpt-4.1")`, `'o3' in model_name`: the code checks a
  name, it doesn't call one. The scan skips the string before `in`, and
  `startswith`, `includes`, `contains` and the like.
- **Prose.** Docstrings and their usage examples, print and log messages, error texts ("pass a model
  such as 'openai/gpt-5.5'"), issue templates' descriptions, benchmark write-ups.
- **Recorded results.** aider's leaderboards and gpt-researcher's benchmark
  JSON carry model names as data and as the commands that were run
  (`aider --model gemini/gemini-1.5-flash-002`), not as calls.
- **URLs.** `https://ai.google.dev/gemini-api/docs/pricing#gemini-2.5-flash`.
- **Providers RadarModel AI didn't read in 0.3.** `azure/gpt-4.1` and 35
  more Azure names in aider's settings. Since 0.4 they are found, as Azure
  models (the counts above are 0.4's).
- **Test placeholders.** `github/o1-mini`, `something/o3-mini`: made-up
  prefixes in aider's tests.
- **Fragments.** `gpt-4-`, `gpt-oss` (no version), a `replace()` target
  built from another name.

Counts of the leftover words, by kind: aider 58 bare, 36 `azure/`, 19
`gemini/` (all in leaderboard command lines), 5 `openai/`, 4 `something/`,
3 `anthropic/`, 2 `github/`; gpt-researcher 41 bare, 2 `o3/`, 2 `models/`;
browser-use 24 bare and one each of `openai/`, `google/`, `anthropic/`.
Every one was read, and every one is in a group above.
