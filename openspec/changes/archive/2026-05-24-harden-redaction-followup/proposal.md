## Why

A follow-up review (manual + codex adversarial) of the actual redaction code found
that `harden-ingest-redaction` closed the structured-JSON-key and connection-string
gaps but still misses two high-signal, common credential shapes:

1. **`Authorization: Basic <base64>` survives.** Only `Bearer <token>` is redacted
   (`redact.go`), so a Basic auth header — whose base64 decodes to `user:pass` — is
   stored verbatim in `messages.content`, `raw_json`, and any derived title.
2. **Cookie headers survive.** `Cookie:` / `Set-Cookie:` values (session ids, CSRF
   tokens) match no shape rule and `cookie` is not a recognized secret key.
3. **`authorization` / `cookie` JSON keys survive.** `isSecretKey` excludes them
   (the `secretKeyRe` set was deliberately narrow), so `{"authorization":"Basic …"}`
   or `{"cookie":"session=…"}` is not redacted by key.

Redaction is a stated privacy guarantee in the README. These are trust bugs, not
polish.

## What Changes

- **Added** redaction rules for `Authorization: Basic <base64>` and for `Cookie:` /
  `Set-Cookie:` header values, redacting only the credential portion.
- **Modified** `isSecretKey` to recognize `authorization`, `cookie`, and `set-cookie`
  as secret-bearing keys, so structured values under them are redacted regardless of
  shape.
- Rules stay conservative (anchored on header shape and `Basic ` + base64) so normal
  prose and searchability are preserved.

## Capabilities

### Modified Capabilities

- `session-ingest`: the "Secret redaction during ingest" requirement is strengthened
  to cover Basic auth headers, cookie headers, and the `authorization`/`cookie` JSON
  keys.
