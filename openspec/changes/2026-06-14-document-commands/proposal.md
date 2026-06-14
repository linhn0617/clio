## Why

clio's docs documented only a subset of commands. `clio ask` (v0.6.0), `clio
activity` and the `--touched`/`--tool`/`--ran` filters (v0.4.0), and `clio recall`
/ `install-hook` / `uninstall-hook` (v0.5.0) all shipped without entries in the
README command examples, the MCP tools tables, or `docs/USAGE.md` — and the
README's MCP tools table was stale (it listed four tools, missing `ask`).

## What Changes

- **Modified** `README.md` and `README_zh-TW.md`: add `ask` to the MCP tools table
  (and refresh `list_sessions` / `activity_summary` for the activity index), add
  `clio ask` / `clio activity` / `clio recall` to the CLI examples, and note the
  opt-in `clio install-hook`.
- **Modified** `docs/USAGE.md`: add a `clio ask` section and a `clio activity`
  section (documenting the activity filters), add the `ask` MCP tool to the tools
  table, and add `recall` / `install-hook` / `uninstall-hook` to "Other commands"
  and the cheat sheet.

No code changes — documentation only.

## Capabilities

### Modified Capabilities

- `documentation`: the README and usage guide SHALL document every user-facing CLI
  command and MCP tool clio ships.
