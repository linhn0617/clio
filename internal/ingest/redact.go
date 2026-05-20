package ingest

import "regexp"

// redactRule pairs a compiled pattern with the replacement applied to matches.
type redactRule struct {
	re   *regexp.Regexp
	repl string
}

// redactRules is a conservative set: high-signal secret shapes only, to avoid
// eating legitimate text (false positives destroy searchability).
var redactRules = []redactRule{
	// PEM private key blocks (multi-line).
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), "[REDACTED:private-key]"},
	// AWS access key id.
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "[REDACTED:aws-key]"},
	// Google API key.
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`), "[REDACTED:gcp-key]"},
	// GitHub tokens (classic + fine-grained + app).
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`), "[REDACTED:github-token]"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`), "[REDACTED:github-token]"},
	// Slack tokens.
	{regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`), "[REDACTED:slack-token]"},
	// OpenAI / Anthropic style keys.
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{20,}\b`), "[REDACTED:api-key]"},
	// Bearer tokens in headers.
	{regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._\-]{12,}`), "Bearer [REDACTED:token]"},
	// KEY=value / KEY: value where KEY names a secret. Value redacted, key kept.
	{regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:SECRET|PASSWORD|PASSWD|API[_-]?KEY|ACCESS[_-]?KEY|PRIVATE[_-]?KEY|TOKEN|CREDENTIAL)[A-Z0-9_]*)\s*([:=])\s*["']?[^\s"']{6,}["']?`), `$1$2[REDACTED:secret]`},
}

// Redact replaces recognized secret patterns in s. It is intentionally
// conservative; see redactRules.
func Redact(s string) string {
	for _, r := range redactRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
