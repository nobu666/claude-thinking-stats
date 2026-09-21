# claude-thinking-stats

How much did Claude Code actually *think*?

Claude Code's extended-thinking plans give you a thinking budget, but nothing shows you how it is spent: the billing page has a cost total, the API usage counters have `output_tokens`, and thinking is buried inside that number. Meanwhile every response's thinking-token count is already on your disk, in the transcripts Claude Code writes under `~/.claude/projects/`. This is a small Go program (standard library only) that reads them and prints the answer.

```
$ claude-thinking-stats
day         requests   output  thinking  think%  zero%  p50    p95  unk  models
2026-09-18        75   62,020    21,009    33.9   34.7  101  1,185    0  claude-opus-5,claude-sonnet-4-6
2026-09-19       128  142,638    64,250    45.0   21.9  146  1,517    0  claude-sonnet-4-6
2026-09-20        60   82,744    51,813    62.6   25.0  127  3,298    0  claude-opus-5,claude-sonnet-4-6
2026-09-21       345  385,577   121,105    31.4   30.4  104  1,346    0  claude-sonnet-4-6
```

No hooks, no API key, no dependencies. It never modifies anything.

## When to reach for this

- **You suspect Claude has stopped thinking as much.** Compare weeks with `-by day`; the `zero%` column is the fastest tell.
- **You are choosing an effort level or a model.** `-by effort` and `-by model` show whether the setting you pay for actually changes how much thinking you get.
- **You just hit a usage limit.** `-top 10` shows which responses burned the most thinking, so you can see what kind of work did it.

If you call the API yourself, you do not need this: the usage block is in every response. If you want dollar amounts, this is not it either; it only counts tokens.

## Install

```
go install github.com/nobu666/claude-thinking-stats@latest
```

With Homebrew:

```
brew install nobu666/tap/claude-thinking-stats
```

Or download a binary for macOS, Linux or Windows from [Releases](https://github.com/nobu666/claude-thinking-stats/releases).

## Usage

```bash
claude-thinking-stats                        # last 7 days, one row per day
claude-thinking-stats -by session -since 30d
claude-thinking-stats -by model -since all
claude-thinking-stats -by effort             # does "high" effort actually think more?
claude-thinking-stats -top 10                # the 10 responses that thought the most
claude-thinking-stats -json                  # same numbers, machine-readable
claude-thinking-stats -subagents             # include subagent transcripts too
claude-thinking-stats ~/.claude/projects/-Users-me-work-app   # one project, or any files/dirs
```

| Column | Meaning |
|---|---|
| `requests` | API responses in the group, one per `message.id` |
| `output` / `thinking` | total output tokens, and how many of them were thinking tokens |
| `think%` | `thinking / output` |
| `zero%` | share of responses with **zero** thinking tokens — the quickest tell that a model or a plan is not thinking |
| `p50` / `p95` | thinking tokens per response, median and 95th percentile |
| `unk` | responses whose usage carried no `thinking_tokens` field at all (older Claude Code versions, models without thinking); counted as 0 |
| `models` | models seen in the group; omitted with `-by model`, where the row key already is the model |

`-since` takes `7d`, `30d`, a `YYYY-MM-DD` date (local time, so it lines up with the `day` rows), or `all`. `-by` takes `day`, `session`, `model`, `project`, `effort`. Flags accept one or two dashes.

## Where the numbers come from

Claude Code appends every API response to `~/.claude/projects/<project>/<session>.jsonl` (or `$CLAUDE_CONFIG_DIR/projects`). Each assistant line carries the API's `usage` block, and in recent Claude Code versions that block includes `output_tokens_details.thinking_tokens`. A response that spans several content blocks is written as several lines with the same `message.id`, so the script counts each id once. Claude Code's own `<synthetic>` placeholder messages (no API call) are skipped. Subagent transcripts live in `<session>/subagents/` and are only read with `--subagents`.

The transcript format is not a documented interface. If a future Claude Code version moves the field, the `unk` column will fill up rather than the numbers silently going wrong.

## What it does not do

- No cost estimates. Prices change; the script sticks to token counts.
- No thinking *content*. Transcripts store only a signature for thinking blocks, not the text.
- No daemon, no hook, no charts. It prints numbers; pipe `-json` into whatever you plot with.

## Development

```
go test ./...
```

Tests run on synthetic transcripts and cover de-duplication, skipped lines, missing fields, the local-time `-since` boundary, the aggregate numbers, percentiles and subagent handling. Releases are built by GoReleaser on a `v*` tag and update the Homebrew tap.

## License

MIT
