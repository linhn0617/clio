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
  corpus of 12,000 inputs maps output markers back to original input positions
  and checks that every byte main removed remains removed. It includes every
  ASCII punctuation prefix, non-ASCII prefixes, bracket noise, nested secret
  JSON, quoted brackets/escapes, query tails and adjacent Basic headers.
- Add GitLab, npm, Hugging Face, Stripe webhook, Slack webhook, PGP private blocks
  and legacy Authorization-token rules with necessary-condition prefilters.
  Stripe webhook bodies accept base64 `+`, `/`, `=`; GitLab token bodies may end
  in `-` or `_`; Slack webhook hosts match case-insensitively.
- Restrict `_PASS` to credential contexts (DB, MYSQL, SMTP, REDIS, ADMIN, ROOT,
  USER, MAIL, FTP, SSH, LDAP, PG, POSTGRES). Add PASSPHRASE and suffixed `_PWD`;
  keep bare PWD and computation-stage names intact.
- Restore connection strings after the existing token, header and KEY rules;
  consume their markers whole to retain the single-marker / no-stray-`]` fix.
  Passwords may contain `/`, `:` and `@` (the last `@` ends the password).
- Redact an AWS id glued to the following key name, `AKIA…secret=…`. The earlier
  report tested the different, already-covered `secret=AKIA…` form.
- Preserve GitLab and Slack documentation placeholders, the Authorization-token
  placeholder, dotted Hugging Face identifiers and lowercase `_sk-learn-…`
  words. Existing standalone sk- coverage stays intact.
- Keep documented debt for quoted free-text passwords with spaces, curl -u,
  .netrc, Docker auths, and truncated PEM/PGP blocks. Address a truncated block
  only if a truncated private key is ever found in the index.

## Impact

- Stored values retain main's redaction coverage and change only toward more
  redaction. The independent 12,000-input main-oracle test is the regression
  evidence: it compares original removed positions, not marker counts.
  No real database or transcript is accessed by these checks.
- Port-like URLs such as `http://localhost:3000@v2` stay redacted as in main: an
  all-digit password cannot be told apart from a port, and keeping real credentials
  redacted outweighs the searchability of these rare strings. Port-plus-path forms
  that main already preserved remain unchanged.
- Existing indexes retain their old text until `clio index --full`.
- No schema, CLI or MCP change.
