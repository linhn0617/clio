# Design — harden-redaction-followup

Umbrella design: `docs/superpowers/specs/2026-05-24-clio-quality-security-batch-design.md` (change ①).

## Approach

Two surfaces, both in `internal/ingest/redact.go`:

1. **Shape regexes (`redactRules`)** — run over free text and over non-JSON prose
   between embedded JSON spans. Add:
   - Basic auth: `(?i)\bBasic\s+[A-Za-z0-9+/=]{8,}` → `Basic [REDACTED:token]`
     (mirrors the existing `Bearer` rule; the `+/=` class matches base64).
   - Cookie header: `(?i)\b(Set-)?Cookie:\s*[^\r\n]+` → `Cookie: [REDACTED:cookie]`
     (redact the whole header value; cookie strings have no safe-to-keep structure).

2. **Structured key matching (`isSecretKey` / `secretKeyRe`)** — add `authorization`,
   `cookie`, `set cookie` to the alternation. `isSecretKey` already normalizes
   `-`/`_`/camelCase to spaces, so `set-cookie`, `Set_Cookie`, `setCookie` all match.

## Why these shapes

`Authorization` and `Cookie` are the two header names that most often carry live
credentials in pasted curl commands, HAR dumps, and request logs — exactly the
content that lands in Claude Code sessions. They were the gap codex found.

## False-positive guard

- Basic rule requires `Basic ` followed by 8+ base64 chars, so the English word
  "basic" in prose is untouched.
- Cookie rule anchors on `Cookie:` / `Set-Cookie:` header form, not the bare word
  "cookie".
- Key matching uses word boundaries (existing `secretKeyRe` behavior), so `cookie`
  matches the key `cookie` but not `cookieJar` → wait: `isSecretKey` splits camelCase,
  so `cookieJar` → `cookie jar` which DOES contain `\bcookie\b`. This is acceptable:
  a key named `cookieJar` plausibly holds a cookie. Documented, not a bug.

## Out of scope

Decoding base64 to redact only the `user:pass` substring — redacting the whole token
is simpler and strictly safer.

Deferred (codex follow-up): credential-passing CLI flags such as `curl -u user:pass`
/ `--user` and `curl -b session=...` / `--cookie`. These are new leak vectors beyond the
verified findings, the short flags (`-u`, `-b`) are FP-prone and need careful design, and
free-text redaction is explicitly best-effort. Tracked as a follow-up rather than expanding
this change.
