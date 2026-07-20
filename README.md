# clio

Search and browse your Claude Code conversation history from the terminal — and let Claude search it too.

English | [繁體中文](./README_zh-TW.md)

clio indexes Claude Code's session files (`~/.claude/projects/*.jsonl`) into a local SQLite + FTS5 database and exposes them through two interfaces:

- **CLI** — `clio search` / `clio ask` / `clio list` / `clio show` to search, answer questions over, and read past conversations across all projects, or `clio tui` for an interactive dashboard
- **MCP server** — lets Claude Code query its own history in-session ("what did we discuss last week?", "how did we fix that bug?")

It is local-first, read-only against your `.claude` data, and never writes to your original session files.

📖 **Full usage guide (bilingual):** [docs/USAGE.md](./docs/USAGE.md)

## Why not just `grep`?

You *could* `grep ~/.claude/projects/*.jsonl` — but that's exactly what clio is built to do well:

- **Memory across sessions.** A fresh Claude Code session can't see past ones. clio gives Claude (and you) a searchable index of every conversation across every project — not just whatever's open now.
- **Ranked by relevance.** Most queries are ordered by relevance (BM25) and recency, so the conversation you meant rises to the top — instead of every line that merely contains the word, in file order. (Very short queries — 1–2 characters, e.g. some CJK words — fall back to a recency-ordered substring scan.)
- **Signal, not noise.** Tool output (file dumps, command logs) is excluded by default and snippets are trimmed, so you — and Claude's context window — get the relevant lines, not megabytes of JSONL.
- **The right session, exactly.** Sessions resolve by id or unambiguous prefix, so you open the one you meant — not whichever file happened to contain the string.

## Getting started

**1. Install the binary**

**Prebuilt (recommended)** — download the asset for your platform from the [latest release](https://github.com/linhn0617/clio/releases/latest) (current: [**v0.13.0**](https://github.com/linhn0617/clio/releases/tag/v0.13.0); macOS/Linux `amd64`+`arm64`, Windows `amd64`), put it on your `PATH`, and verify it against `SHASUMS256.txt`. The binaries are not
code-signed: on macOS the first run may be blocked by Gatekeeper — allow it under
System Settings → Privacy & Security, or clear the quarantine flag with
`xattr -d com.apple.quarantine <path-to-clio>`.

**Or with `go install`:**

```
go install github.com/linhn0617/clio/cmd/clio@v0.13.0   # or @latest for the newest
```

This drops `clio` into `$(go env GOPATH)/bin` — make sure that's on your `PATH`.

**2. Index your history and register the MCP server**

```
clio install-mcp
```

This does two things, in order:
1. Builds the full index from `~/.claude/projects/` (shows progress).
2. Only if indexing succeeds, registers clio in `~/.claude.json` — atomically, preserving your other MCP servers and leaving a `.bak` of the previous version for manual recovery.

**3. Restart Claude Code**

Then ask it about your past work:

> "What did we work on last week?"
> "How did we fix that auth bug a while back?"
> "Find where we discussed the database migration."

Claude calls clio over MCP to answer. While Claude Code runs, clio's MCP server watches `~/.claude/projects/` and keeps the index current automatically.

**4. (Optional) Use the CLI directly**

```
clio tui                        # interactive dashboard: search/browse/activity/ask
clio search "驗證 流程"          # full-text search (CJK + code)
clio ask "how did we fix that auth bug"   # cited evidence bundle for a question (no generation)
clio list --since 7d --touched auth.ts    # browse recent sessions, filter by activity
clio show <session-id>          # print a full conversation (markdown|json|raw)
clio show <id> --include-subagents         # ...and inline its Claude Code subagent transcripts
clio activity --by file --since 7d         # files touched / commands run / tools used
clio search "race" --source codex          # also index & search OpenAI Codex CLI history (opt-in; default: Claude Code only)
clio activity --by command --source codex  # ...and break down Codex commands / files / tools too
clio recall                                # recent-activity digest for the current project
clio doctor                     # health check
```

To remove the integration later: `clio uninstall-mcp`.

Want each new session to open with a recent-activity digest? Opt in with `clio install-hook` (undo with `clio uninstall-hook`).

## How indexing stays current

- **MCP server running** (Claude Code open): a file watcher live-ingests new activity; you never run anything manually.
- **MCP server not running**: each CLI command does a quick incremental catch-up before querying. Run `clio index` anytime to force one; `clio index --full` rebuilds from scratch.

Short queries (1-2 characters, e.g. most CJK words) automatically fall back to a substring scan, since the trigram index needs 3+ characters.

## MCP tools

When registered via `clio install-mcp`, Claude Code can call:

| Tool | What it does |
|------|--------------|
| `search` | Full-text search, **ranked** by relevance + recency (short queries fall back to a substring scan; tool output excluded by default) |
| `ask` | Answer a question from history: a cited bundle of the most relevant excerpts, windowed in their turns and grouped by session, for Claude to synthesize from; `max_tokens` bounds the bundle's estimated size (default 2000, min 200, max 8000) |
| `list_sessions` | List sessions by date/project/turn count, or by file touched / tool used / command run; subagent children are hidden by default (each parent carries a `subagent_count`), `include_subagents` adds them as rows with `parent_session` / `agent_type` |
| `activity_summary` | Counts by day or project, or your most-used files / commands / tools / patterns / URLs ("what did I touch last week?") |
| `read_session` | Read one session in full, paginated; reports a parent's subagents (`include_subagents` inlines them) |

Retrieval quality (ranking, FTS/LIKE tiering, `ask` grouping and windowing) is protected end to end by a deterministic regression suite over a small bilingual fixture corpus (`internal/eval`, runs under plain `go test ./...`).

## Privacy

- Read-only access to `~/.claude/projects/`; original files are never modified.
- Secret redaction is pattern-based and best-effort: high-signal shapes (API keys, tokens, private keys, `.env` lines) are redacted at ingest time, in both the searchable text and the stored raw event. Free-form secrets that match no known pattern are not caught.
- Registering clio as an MCP server is an all-or-nothing grant: any client you register it with can read your entire indexed history through its tools.
- All data stays on your machine; no telemetry, no cloud sync. The database lives at `~/Library/Application Support/clio/db.sqlite` (macOS) or `~/.local/share/clio/db.sqlite` (Linux), with `0600` permissions.

## License

[MIT](./LICENSE)
