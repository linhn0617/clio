# Round-three verification

All commands used the installed Go 1.26.0 binary directly, GOPROXY=off,
GOSUMDB=off, GOTOOLCHAIN=local, and checkout-local GOCACHE/TMPDIR. No network,
real database, commit or push was used.

## Red checks and reverted mutations

Items 1 and 3–7 failed before production edits. The supplied full URL examples
for item 2 already matched main on this Go toolchain; an additional focused rule
forces the ASCII/Unicode boundary and fails with slice re-matching. Both full URL
examples remain checked against main. Each temporary mutation below was run
individually against its targeted test and reverted immediately afterward.

| Item | Initial failing assertion (quoted) | Mutation and observed failure (quoted) |
| --- | --- | --- |
| 1 | “glued coverage lost at byte 37” | Disable overlapping key/header protection: “glued coverage lost at byte 37” |
| 2 | “original boundary lost” with the old slice replacement | Re-match the match slice instead of expanding original indices: “original boundary lost” |
| 3 | “whole body leaked”; “placeholder changed” | Restore suffix/substring placeholder acceptance: “whole body leaked”; “placeholder changed” |
| 4 | “prefixed credential leaked”; “structured credential leaked” | Remove optional context prefixes from text and structured rules: both assertions fail |
| 5 | “port path changed” for web, registry and IPv6 | Disable the extended-form port guard: “port path changed” for web and registry |
| 6 | “overlong AWS changed” | Remove the AWS terminator and preserved suffix capture: “overlong AWS changed” |
| 7 | “working directory changed” for text and JSON | Restore arbitrary suffixed PWD in both checks: “working directory changed” for text and JSON |

The expanded oracle checks 12,000 generated JSON/order inputs and 3,000
cross-product inputs: 50 token variants × 15 key/header forms × 4 separators
(including no separator). It compares original input positions removed by the
frozen main oracle, requiring zero retained positions that main removed.
A further 20,000 randomized inputs compare every span against the unchanged
b7517e5 pre-scan, independently of redaction output, for the memory-only rewrite.

An additional red-first guard regression reported “standalone token hidden by
word guard at byte 14”: an underscore-prefixed prose-shaped match contained a
later standalone sk-style token. The guard now rejects preservation when that
would hide main's standalone token coverage.

Four further red-first cases reported “connection coverage lost” when a new
match consumed a following connection scheme, or a widened PASSPHRASE key
preserved a scheme name. Original connection removals now join the same linear
coverage merge; a captured key name overlapping such a removal is suppressed.
The complete ingest suite passes with both additional regressions.

## Acceptance checks

- `gofmt -l .`: empty after removing temporary build artifacts.
- `go vet ./...`: silent.
- `go test -count=1 ./...`: all pass except the existing TestProjectRoot failure.
  Its no-git temporary directory inherits this checkout's git root because the
  sandbox TMPDIR is inside the checkout. The test was left unchanged.
- `go test -race -count=1 ./internal/ingest/`: passed (440.847 seconds).
- `OPENSPEC_TELEMETRY=0 DO_NOT_TRACK=1 openspec validate --all --strict`:
  18 passed, 0 failed.

## Benchmarks

Apple M1, darwin/arm64, Go 1.26.0; `-run '^$' -bench ... -benchmem -count=1`.

| Benchmark | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| StripPrivateNested2000 | 602158 | 424421 | 55 |
| RedactStringBracketFlood | 1431897 | 4143137 | 18022 |
| RedactStringNestedObjects | 363755 | 726531 | 3616 |
| BalancedJSONSparseText/1024 | 2225 | 96 | 2 |
| BalancedJSONSparseText/5242880 | 16685671 | 96 | 2 |

Sparse text allocation stays at 96 bytes for both 1 KB and 5 MB inputs containing
one trailing opener. Time still scales with input length, as required by a scan.
Overlapping token/key/header removals can conservatively consume the adjacent
key/header name as well as its credential; this is intentional additional redaction.
