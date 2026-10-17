## 1. Prefilter (B1)

- [x] 1.1 Tests first: filtered vs unfiltered equivalence (known cases, every literal of
      `redact_test.go`, Unicode fold and `\b` cases, seeded random, fuzz target), table-sync test,
      non-ASCII fold test, `k`/`s` fold pin
- [x] 1.2 `redactRule` gains `needles` / `also` / `foldCase` / `runAlways`; `Redact` skips rules whose
      needle is absent; `redactUnfiltered` is the reference path
- [x] 1.3 Mutation check: breaking the non-ASCII fallback, a needle, or the `passwd` needle turns
      the equivalence tests red

## 2. Single pass (B2)

- [x] 2.1 Inventory of every `redactString` call site; show which repeated passes change output
      (`TestRedactNotIdempotentOnAdversarialText`, `TestRedactStringNotIdempotentOnRedactedJSONKey`)
- [x] 2.2 `redactMemo` shared by tool summary and activity target (Claude and Codex paths), with a
      test that the memoized results equal the unmemoized ones

## 3. Verification

- [x] 3.1 `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`
- [x] 3.2 `openspec validate --all --strict`
- [x] 3.3 Real-corpus comparison (index `content` and `raw_json`, plus unredacted source transcripts)
- [x] 3.4 `BenchmarkIngestFullIndex` and real-binary `clio index --full` wall clock, before and after
