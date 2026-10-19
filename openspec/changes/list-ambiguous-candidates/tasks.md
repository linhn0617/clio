## 1. Ambiguous prefix candidates

- [x] 1.1 Add failing tests: candidate cap, newest-first order, more-line, terminal
      escape and newline stripping, distinguishing id length.
- [x] 1.2 Return `*AmbiguousError` (matches `ErrAmbiguous`) listing the candidates.

## 2. Tidy

- [x] 2.1 Delete `AddSessionStartHook` / `RemoveSessionStartHook`; point their tests
      at `AddClioHooks` / `RemoveClioHooks` and rename them after the behavior.
- [x] 2.2 Add the `usage` and `prune-raw` lines to README_zh-TW.md.

## 3. Verification

- [x] 3.1 gofmt, go vet, go test ./..., openspec validate --all --strict.
