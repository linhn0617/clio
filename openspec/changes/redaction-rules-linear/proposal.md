## Why

PR #57 adds credential rules and removes superlinear scans in ingest redaction.
The initial JSON boundary filter and failed-probe cap reduced main's coverage:
embedded JSON after punctuation, words, or many invalid brackets could expose
secret-keyed values. Moving connection strings before the existing rules also
hid query-key names and Authorization headers, exposing credential tails.

## What Changes

- Replace JSON boundary heuristics and probe caps with a linear, escape-aware
  balanced-span pre-scan. Mismatched closers terminate candidates. Two quote
  contexts preserve main's probes even inside quoted prose. Validate shallow
  syntax bottom-up once per balanced span so nested invalid spans cannot cause
  repeated decoding; decode valid outermost candidates first and skip successful
  spans. No punctuation or failed-probe-count restriction remains.
- Retain the linear private-tag scanner and its independent reference tests.
- Keep an independent test-only copy of commit 165f184's redactor. A generated
  corpus of 12,000 inputs plus token/key/header cross-products maps output markers back to original input positions
  and checks that every byte main removed remains removed. It includes every
  ASCII punctuation prefix, non-ASCII prefixes, bracket noise, nested secret
  JSON, quoted brackets/escapes, query tails and adjacent Basic headers. The
  cross-product covers every token rule against credential keys, Basic, legacy
  token, Bearer and Cookie headers, glued and separated, at multiple body lengths.
- Add GitLab, npm, Hugging Face, Stripe webhook, Slack webhook, PGP private blocks
  and legacy Authorization-token rules with necessary-condition prefilters.
  Stripe webhook bodies accept base64 `+`, `/`, `=`; GitLab token bodies may end
  in `-` or `_`; Slack webhook hosts match case-insensitively.
- Restrict `_PASS` to credential contexts (DB, MYSQL, SMTP, REDIS, ADMIN, ROOT,
  USER, MAIL, FTP, SSH, LDAP, PG, POSTGRES). Add PASSPHRASE;
  restrict `_PWD` to the same contexts, allow prefixes such as MY_DB_PASS and
  APP_ROOT_PWD, and keep directory/computation-stage names intact.
- Restore connection strings after the existing token, header and KEY rules;
  consume their markers whole to retain the single-marker / no-stray-`]` fix.
  Keep main's password alphabet in the original rule. A separate rule accepts
  empty users and passwords containing `/` or `:`, while excluding numeric
  port-plus-path forms (including single-label hosts and IPv6).
- Redact an AWS id glued to the following key name, `AKIA…secret=…`. The earlier
  report tested the different, already-covered `secret=AKIA…` form. Require a
  word boundary or a captured lowercase key start; preserve overlong AWS examples.
- Preserve GitLab and Slack documentation placeholders, the Authorization-token
  placeholder only for whole placeholder bodies, dotted Hugging Face identifiers and lowercase `_sk-learn-…`
  words. A guarded prose match containing a later standalone sk- token is
  redacted so that the guard cannot hide main's standalone coverage.
- Evaluate guarded matches and expand templates using original submatch indices,
  preserving Unicode boundary context. Evaluate overlapping key/header coverage
  before token replacement and coalesce removals so absorbed names cannot leak
  their values. Include original connection spans so consumed URL schemes cannot
  hide credentials; suppress a kept key name when it overlaps such a span. This
  can conservatively remove an adjacent key/header name too.
- Store balanced spans sparsely by opener offset, and defer shallow text copies
  until a parent needs child substitution. Non-bracket text no longer requires
  a per-byte span table or a copy for an unclosed/childless candidate.
- Keep documented debt for quoted free-text passwords with spaces, curl -u,
  .netrc, Docker auths, and truncated PEM/PGP blocks. Address a truncated block
  only if a truncated private key is ever found in the index.

## Impact

- Stored values retain main's redaction coverage and change only toward more
  redaction. The independent 15,000-input main-oracle tests provide the regression
  evidence: it compares original removed positions, not marker counts, across
  arbitrary JSON prefixes, quote/escape and bracket noise, nested secret values,
  connection query/header order, glued/separated token × key/header pairs, nested standalone tokens in guarded
  prose, and token/credential-key matches overlapping URL schemes.
  No real database or transcript is accessed by these checks.
- Port-like URLs such as `http://localhost:3000@v2` stay redacted as in main: an
  all-digit password cannot be told apart from a port, and keeping real credentials
  redacted outweighs the searchability of these rare strings. Port-plus-path forms
  that main already preserved remain unchanged.
- Existing indexes retain their old text until `clio index --full`.
- No schema, CLI or MCP change.
