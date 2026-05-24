# clio

Search and browse your Claude Code conversation history from the terminal — and let Claude search it too.

English | [繁體中文](./README_zh-TW.md)

clio indexes Claude Code's session files (`~/.claude/projects/*.jsonl`) into a local SQLite + FTS5 database and exposes them through two interfaces:

- **CLI** — `clio search`, `clio list`, `clio show` for searching and reading past conversations across all projects
- **MCP server** — lets Claude Code query its own history in-session ("what did we discuss last week?", "how did we fix that bug?")

It is local-first, read-only against your `.claude` data, and never writes to your original session files.

📖 **Full usage guide (bilingual):** [docs/USAGE.md](./docs/USAGE.md)

## Getting started

**1. Install the binary**

**Prebuilt (recommended)** — download the asset for your platform from the [latest release](https://github.com/linhn0617/clio/releases/latest) (current: [**v0.2.0**](https://github.com/linhn0617/clio/releases/tag/v0.2.0); macOS/Linux `amd64`+`arm64`, Windows `amd64`), put it on your `PATH`, and verify it against `SHASUMS256.txt`. This repo is private, so download while signed in to GitHub.

**Or with `go install`:**

```
export GOPRIVATE=github.com/linhn0617/*   # private repo: set once
go install github.com/linhn0617/clio/cmd/clio@v0.2.0   # or @latest for the newest
```

This drops `clio` into `$(go env GOPATH)/bin` — make sure that's on your `PATH`, and that your git/GitHub auth can clone the private repo.

**2. Index your history and register the MCP server**

```
clio install-mcp
```

This does two things, in order:
1. Builds the full index from `~/.claude/projects/` (shows progress).
2. Only if indexing succeeds, registers clio in `~/.claude.json` — atomically, with a `.bak` backup, preserving your other MCP servers.

**3. Restart Claude Code**

Then ask it about your past work:

> "What did we work on last week?"
> "How did we fix that auth bug a while back?"
> "Find where we discussed the database migration."

Claude calls clio over MCP to answer. While Claude Code runs, clio's MCP server watches `~/.claude/projects/` and keeps the index current automatically.

**4. (Optional) Use the CLI directly**

```
clio search "驗證 流程"          # full-text search (CJK + code)
clio search "bug" --since 7d --project myapp --json
clio list --since 7d            # browse recent sessions
clio show <session-id>          # print a full conversation (markdown|json|raw)
clio doctor                     # health check
```

To remove the integration later: `clio uninstall-mcp`.

## How indexing stays current

- **MCP server running** (Claude Code open): a file watcher live-ingests new activity; you never run anything manually.
- **MCP server not running**: each CLI command does a quick incremental catch-up before querying. Run `clio index` anytime to force one; `clio index --full` rebuilds from scratch.

Short queries (1-2 characters, e.g. most CJK words) automatically fall back to a substring scan, since the trigram index needs 3+ characters.

## MCP tools

When registered via `clio install-mcp`, Claude Code can call:

| Tool | What it does |
|------|--------------|
| `search` | Full-text search across all conversations (tool output excluded by default) |
| `list_sessions` | List sessions with date/project/turn filters |
| `activity_summary` | Counts grouped by day or project ("what did I do last week?") |
| `read_session` | Read one session in full, paginated |

## Privacy

- Read-only access to `~/.claude/projects/`; original files are never modified.
- Secret patterns (API keys, tokens, private keys, `.env` lines) are redacted at ingest time, in both the searchable text and the stored raw event.
- All data stays on your machine; no telemetry, no cloud sync. The database lives at `~/Library/Application Support/clio/db.sqlite` (macOS) or `~/.local/share/clio/db.sqlite` (Linux), with `0600` permissions.

## License

[MIT](./LICENSE)
