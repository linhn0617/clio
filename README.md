# clio

Search and browse your Claude Code conversation history from the terminal — and let Claude search it too.

clio indexes Claude Code's session files (`~/.claude/projects/*.jsonl`) into a local SQLite + FTS5 database and exposes them through two interfaces:

- **CLI** — `clio search`, `clio list`, `clio show` for searching and reading past conversations across all projects
- **MCP server** — lets Claude Code query its own history in-session ("what did we discuss last week?", "how did we fix that bug?")

It is local-first, read-only against your `.claude` data, and never writes to your original session files.

## Status

Early development. See `openspec/changes/add-cli-and-mcp-foundation/` for the design and implementation plan.

## Install

```
go install github.com/linhn0617/clio/cmd/clio@latest
```

(Cross-platform release binaries: planned.)

## Usage

```
clio index                      # build / update the index
clio search "驗證 流程"          # full-text search (CJK + code)
clio list --since 7d            # browse recent sessions
clio show <session-id>          # print a full conversation
clio install-mcp                # index + register clio in ~/.claude.json
clio doctor                     # health check
```

## Privacy

- Read-only access to `~/.claude/projects/`; original files are never modified
- Secret patterns (API keys, tokens, private keys, `.env` lines) are redacted at ingest time
- All data stays on your machine; no telemetry, no cloud sync

## License

TBD
