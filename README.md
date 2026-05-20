# clio

Search and browse your Claude Code conversation history from the terminal — and let Claude search it too.

clio indexes Claude Code's session files (`~/.claude/projects/*.jsonl`) into a local SQLite + FTS5 database and exposes them through two interfaces:

- **CLI** — `clio search`, `clio list`, `clio show` for searching and reading past conversations across all projects
- **MCP server** — lets Claude Code query its own history in-session ("what did we discuss last week?", "how did we fix that bug?")

It is local-first, read-only against your `.claude` data, and never writes to your original session files.

## Install

```
go install github.com/linhn0617/clio/cmd/clio@latest
```

Or download a prebuilt binary from the [Releases](https://github.com/linhn0617/clio/releases) page (macOS/Linux/Windows, amd64/arm64).

## Quick start

```
clio install-mcp                # index your history + register clio in ~/.claude.json
```

Restart Claude Code, then ask it things like "what did we work on last week?" or
"how did we fix that auth bug?". Claude will query clio over MCP.

## CLI usage

```
clio index                      # build / update the index
clio search "驗證 流程"          # full-text search (CJK + code)
clio search "bug" --since 7d --project myapp --json
clio list --since 7d            # browse recent sessions
clio show <session-id>          # print a full conversation (markdown|json|raw)
clio doctor                     # health check
clio uninstall-mcp              # remove clio from ~/.claude.json
```

Short queries (1-2 characters, e.g. most CJK words) automatically fall back to a
substring scan, since the trigram index needs 3+ characters.

## MCP tools

When registered via `clio install-mcp`, Claude Code can call:

| Tool | What it does |
|------|--------------|
| `search` | Full-text search across all conversations (tool output excluded by default) |
| `list_sessions` | List sessions with date/project/turn filters |
| `activity_summary` | Counts grouped by day or project ("what did I do last week?") |
| `read_session` | Read one session in full, paginated |

## Privacy

- Read-only access to `~/.claude/projects/`; original files are never modified
- Secret patterns (API keys, tokens, private keys, `.env` lines) are redacted at ingest time
- All data stays on your machine; no telemetry, no cloud sync

## License

TBD
