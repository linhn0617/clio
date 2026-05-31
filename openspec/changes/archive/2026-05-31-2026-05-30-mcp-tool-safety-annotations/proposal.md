## Why

clio's four MCP tools (`search`, `list_sessions`, `activity_summary`,
`read_session`) are all read-only queries against the local session index, but
they register without `ToolAnnotation` hints. Per the MCP spec, clients treat an
unannotated tool as worst-case — destructive + open-world — so Claude Code's
`/mcp` panel labels every clio tool `[destructive] [open-world]`. That's
misleading and adds visual noise to a panel users consult to decide whether to
trust a tool.

## What Changes

- **Modified** the MCP server to attach `readOnlyHint=true`,
  `destructiveHint=false`, `idempotentHint=true`, and `openWorldHint=false`
  annotations to each of the four registered tools. No behavior change in tool
  inputs, outputs, or handlers — only the tool metadata advertised at
  `list_tools` time.

## Capabilities

### Modified Capabilities

- `mcp-server`: the four tools advertise safety hints so clients display them as
  read-only rather than the spec's worst-case default.
