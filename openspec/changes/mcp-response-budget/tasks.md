## 1. Response budget

- [x] 1.1 Tests first: truncation at row boundary with `truncated`/`next` per tool, `max_chars`
      clamp, lone oversized row, default below 50k, subagent-message last resort
- [x] 1.2 `fitBudget` (serialized-size binary search over row count) and `max_chars` parameter on
      all five tools

## 2. read_session modes

- [x] 2.1 Tests first: `last_n`, `around_sequence` ranges and edges, mutual exclusion error,
      `offset: 0` not a mode, `total`/`offset` reporting
- [x] 2.2 `sessions.CountMessages`, mode selection in `handleReadSession`, tool schema

## 3. Docs and specs

- [x] 3.1 README.md / README_zh-TW.md
- [x] 3.2 Spec delta for mcp-server

## 4. Verification

- [x] 4.1 `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`, `openspec validate --all --strict`
