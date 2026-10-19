## 1. Ambiguous prefix candidates

- [x] 1.1 Add failing tests: candidate cap, newest-first order, more-line, terminal
      escape and newline stripping, distinguishing id length.
- [x] 1.2 Return `*AmbiguousError` (matches `ErrAmbiguous`) listing the candidates.

## 2. Tidy

- [x] 2.1 Delete `AddSessionStartHook` / `RemoveSessionStartHook`; point their tests
      at `AddClioHooks` / `RemoveClioHooks` and rename them after the behavior.
- [x] 2.2 Add the `usage` and `prune-raw` lines to README_zh-TW.md.

## 3. Review fixes

- [x] 3.1 Reject an empty id before querying.
- [x] 3.2 List top-level sessions before subagents and tag subagents.
- [x] 3.3 Choose each shown id by resolving it against the whole index (not only
      the listed candidates), skipping prefixes that are another session's exact id.
- [x] 3.4 Show non-printable ids quoted and escaped; cut ids on rune boundaries.
- [x] 3.5 Keep the two-row fast path; run the ordered query only when ambiguous.
- [x] 3.6 Render per surface: `clio show` with list formatting, MCP with RFC 3339.
- [x] 3.7 Move the one-line title rendering to `termsafe.OneLine`.
- [x] 3.8 Delete `AddPreToolUseReadHook` / `InstallPreToolUseReadHook` /
      `RemovePreToolUseReadHook`; migrate their tests to the live entry points.
- [x] 3.9 Test that a recall-only install leaves foreign PreToolUse hooks intact.
- [x] 3.10 Mutation-check each fix (each mutation turns a named test red).

## 4. Verification

- [x] 4.1 gofmt, go vet, go test ./..., openspec validate --all --strict.
