## 1. Secret rules (B6)

- [x] 1.1 Tests first (red on main): positive cases with exact expected output for each new
      shape, an untouched-text guard list, the four reported defects (`a://u:p@sk-…` stray `]`,
      `x_sk-…`, AWS key inside `secret=` as a guard, `ſecret=` at start / after a space)
- [x] 1.2 New rules with prefilter needles: `glpat-`, `npm_`, `hf_`, `whsec_`, Slack webhook URL,
      PGP private key block, `Authorization: token`
- [x] 1.3 Widened rules: empty-user connection string, `_PASS` / `MYSQL_PWD` keys, `sk-` after `_`,
      key boundary `\b` or a consumed non-word character; connection-string rule moved ahead of
      the token rules
- [x] 1.4 `// debt:` above the rule table for the four shapes not covered
- [x] 1.5 Prefilter equivalence test for the new shapes, new fragments in the random generator

## 2. Linear time (B5)

- [x] 2.1 `stripPrivate`: tag scan + one stack pass + right-to-left table; the old search kept in
      `redact_linear_test.go` as `stripPrivateReference`; equality on hand cases, 300k random
      inputs and a fuzz target; negative control (treating an opener that swallows a closer as an
      opener) turns it red
- [x] 2.2 `redactString`: probe only at boundary characters, at most 128 failed probes per string;
      equality with the probe-everything reference for inputs inside those limits
- [x] 2.3 Adversarial tests (`TestAdversarialInputsStayLinear`, 500 ms ceiling) and three
      benchmarks (nested `<private>`, `[` flood, nested `{"a":`)

## 3. Verification

- [x] 3.1 `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`, `go test -race -count=1 ./internal/ingest/`
- [x] 3.2 `openspec validate --all --strict`
- [x] 3.3 Real-corpus comparison, read-only (index `content` and `raw_json`): `stripPrivate` equal to
      the previous implementation; `redactString` equal to the probe-everything reference; per-rule
      hit counts for the B6 rules (numbers in the change report)
