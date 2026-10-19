## 1. Red-first regression coverage

- [x] 1.1 Add and run failing tests for unrestricted embedded JSON, probe floods,
      whole JSON values, rule alphabets, key names, URL credentials and guards.
- [x] 1.2 Embed an independent main oracle from commit 165f184 and compare removed
      original positions across at least 10,000 generated inputs.
- [x] 1.3 Remove the old boundary-only comparison assumption and boundary tests.

## 2. Implementation

- [x] 2.1 Retain linear private-tag scanning and reference equivalence tests.
- [x] 2.2 Implement linear escape-aware balanced-span scanning and bottom-up
      shallow syntax validation; decode outermost valid spans without caps.
- [x] 2.3 Restrict credential-context _PASS; add PASSPHRASE and credential-context _PWD, including prefixed contexts.
- [x] 2.4 Correct Stripe base64, GitLab trailing dash and Slack host case.
- [x] 2.5 Restore rule order, consume prior markers whole, allow URL password
      slashes/colons/at-signs, and cover adjacent Basic headers and query tails.
- [x] 2.6 Redact AWS ids adjacent to secret key names; add placeholder and
      identifier guards; document truncated-key debt without implementing it.

## 3. Verification

- [x] 3.1 Run temporary mutations for each regression, record failures and revert.
- [x] 3.2 Run gofmt, go vet, all tests, ingest race tests, and three adversarial
      benchmarks; retain the 500 ms adversarial ceiling.
- [x] 3.3 Run openspec validate --all --strict with telemetry disabled.
- [x] 3.4 Port-like URLs (`http://host:3000@v2`): keep main's redaction. An all-digit
      "password" is indistinguishable from a port (`redis://:123456@host` is a real
      credential), so the port-only false-positive guards were dropped in favour of
      main's coverage; port-plus-path forms main left intact stay intact.

## 4. Round-three boundary and memory corrections

- [x] 4.1 Add failing regressions for glued token/key/header coverage, original
      Unicode match context, whole-token placeholders, prefixed PASS, connection
      authority boundaries, AWS termination and directory PWD guards.
- [x] 4.2 Evaluate overlapping key/header removals before consuming their names;
      expand replacements using original-string submatch indices.
- [x] 4.3 Restrict placeholders to whole bodies and PASS/PWD to credential contexts;
      preserve AWS boundaries and main's connection password alphabet.
- [x] 4.4 Replace per-byte span storage with sparse opener offsets and defer
      shallow text copies; benchmark 1 KB versus 5 MB sparse-bracket text.
- [x] 4.5 Rename review-number tests to behavior names; document the frozen oracle
      baseline, categories, refresh/retirement policy and balanced-span parser path.
- [x] 4.6 Run and revert seven focused mutations; record failure quotes and final
      acceptance commands in verification.md, including the sandbox TestProjectRoot
      exception and three adversarial benchmark results.
- [x] 4.7 Add red-first oracle regressions for a standalone token hidden inside a
      guarded underscore word and for new matches consuming connection schemes;
      merge original connection removals and suppress overlapping kept key names.
