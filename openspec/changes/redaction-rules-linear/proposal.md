## Why

Two follow-ups to the redaction prefilter (#55):

- **B6, secret rules.** The rule table misses several common credential shapes (GitLab, npm,
  Hugging Face, Stripe webhook, Slack incoming webhook, PGP private keys, GitHub's legacy
  `Authorization: token`, `redis://:pass@host`, `MYSQL_PWD`, `DB_PASS`/`*_PASS`), and the
  verifier of #55 found four defects in the existing rules (a stray `]` after a nested token,
  `x_sk-…` not matched, an ASCII-only `\b` that misses a key starting with U+017F LONG S, and
  an AWS-key-inside-`secret=` report).
- **B5, algorithmic cost.** `stripPrivate` re-searched for a closer at every nested opener
  (2000 openers + one closer on one 18 KB line: 5.1 s here, 18.5 s on the maintainer's index),
  and `redactString` probed a JSON decoder at every `{`/`[` (18 KB of `[`: 1.4 s). Both are
  reachable from any transcript line, so one hostile or unlucky tool output could stall an index.

## What Changes

- **session-ingest (B6)**: new rules, each with prefilter needles and tests (positive, negative,
  over-redaction guard): `glpat-`, `npm_`, `hf_`, `whsec_`, Slack incoming-webhook URL, PGP
  private key block, `Authorization: token <x>`. Existing rules widened: connection strings with an
  empty user, `KEY=value` where the key ends in `_PASS` or is `MYSQL_PWD`, `sk-` directly after
  an underscore, and a `KEY=value` key that starts with U+017F/U+212A (the start boundary is now
  `\b` or a consumed non-word character). The connection-string rule moved ahead of the token
  rules so a token used as a URL password yields one `[REDACTED:connstring]` marker instead of
  `[REDACTED:connstring]]`.
- Not done on purpose (kept as a `debt:` above the rule table): quoted passwords containing
  spaces, `curl -u user:pass`, `.netrc`, Docker `auths` blocks.
- Reported but not reproduced: "an AWS key id inside a `secret=` value is not redacted". Every
  wrapper tried (25 forms, plus 400k random wrappers) redacts; a guard test pins it.
- **session-ingest (B5)**: `stripPrivate` is single-pass (tag scan, one stack pass, one
  right-to-left table) with output byte-identical to the previous search (the old code stays in
  the tests as an oracle, compared on hand cases, 300k random inputs and a fuzz target).
  `redactString` probes for embedded JSON only at a bracket that follows the text start,
  whitespace, `:`, `,`, `=`, `(`, a quote, a backtick or a bracket, and stops after 128 failed
  probes per string (`debt:` documents the trade-off past the cap). The quote, backtick and
  bracket characters go beyond the minimal set first proposed: on the maintainer's
  index the minimal set left 8 of 88k messages with fewer redaction markers than probing every
  bracket (shell `-d '{…}'`, markdown spans).

## Impact

- Stored values change only where a new or widened rule now matches (more redaction). Existing
  indexes keep their old text until `clio index --full`.
- Embedded JSON glued to a word or other punctuation (`x{"apiKey":"…"}`, `f(){…}`) is no longer
  found by the JSON probe; the shape regexes still run on it. Corpus numbers are in the change
  report.
- No schema, CLI or MCP change.
