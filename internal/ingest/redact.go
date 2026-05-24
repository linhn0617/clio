package ingest

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

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
	// Stripe keys (underscore form: sk_live_, pk_live_, rk_live_, _test_).
	{regexp.MustCompile(`\b[rsp]k_(?:live|test)_[A-Za-z0-9]{10,}\b`), "[REDACTED:stripe-key]"},
	// JWTs (header.payload.signature, base64url).
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), "[REDACTED:jwt]"},
	// Bearer tokens in headers.
	{regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._\-]{12,}`), "Bearer [REDACTED:token]"},
	// KEY=value / KEY: value where KEY names a secret. Value redacted, key kept.
	{regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:SECRET|PASSWORD|PASSWD|API[_-]?KEY|ACCESS[_-]?KEY|PRIVATE[_-]?KEY|TOKEN|CREDENTIAL)[A-Z0-9_]*)\s*([:=])\s*["']?[^\s"']{6,}["']?`), `$1$2[REDACTED:secret]`},
	// Connection strings: scheme://user:pass@host (requires user:pass@ authority).
	// Stop at common delimiters so trailing punctuation like ),;> is preserved.
	{regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]*://[^\s:/@]+:[^\s:/@]+@[^\s'")>\]},;]+`), "[REDACTED:connstring]"},
}

// Redact replaces recognized secret patterns in s. It is intentionally
// conservative; see redactRules.
func Redact(s string) string {
	for _, r := range redactRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

// secretKeyRe matches secret-named keys with word boundaries to avoid false
// positives (e.g. "tokenizer", "author", "oauth_provider").
// Compound terms use spaces because isSecretKey normalizes underscores to spaces.
var secretKeyRe = regexp.MustCompile(`\b(password|passwd|secret|secrets|credential|credentials|token|apikey|api key|accesskey|access key|privatekey|private key|secret key|auth token|dsn|connection string|conn str)\b`)

// camelBoundaryRe matches the boundary between a lower/digit and an upper char
// so "secretKey" → "secret Key" and "dbPassword" → "db Password".
var camelBoundaryRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// isSecretKey returns true if the key name looks like a secret holder.
// Underscores and hyphens are treated as word separators; camelCase is split
// so that "db_password", "dbPassword", and "api-key" all match.
func isSecretKey(k string) bool {
	n := camelBoundaryRe.ReplaceAllString(k, "$1 $2")
	n = strings.NewReplacer("_", " ", "-", " ").Replace(n)
	n = strings.ToLower(n)
	return secretKeyRe.MatchString(n)
}

// redactString is the single text redactor used for message content, session
// titles, and string leaves inside JSON.
//
// If the (trimmed) string looks like a JSON object or array, it is parsed and
// walked structurally; otherwise the shape regexes (Redact) are applied.
func redactString(s string) string {
	t := strings.TrimSpace(s)
	if len(t) > 0 && (t[0] == '{' || t[0] == '[') {
		if red, ok := redactJSONValue([]byte(t)); ok {
			return red
		}
	}
	return Redact(s)
}

// redactJSON walks a raw JSON event line and redacts secret values structurally.
// On decode failure it falls back to text Redact.
func redactJSON(line []byte) []byte {
	if result, ok := redactJSONValue(line); ok {
		return []byte(result)
	}
	return []byte(Redact(string(line)))
}

// redactJSONValue decodes b as JSON, walks it with redactWalk, and re-encodes
// it. Returns ("", false) if b is not valid JSON.
func redactJSONValue(b []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	// Require EOF: trailing non-whitespace bytes mean this is not pure JSON.
	// Fall back to text Redact so trailing bytes are not silently dropped.
	if _, err := dec.Token(); err != io.EOF {
		return "", false
	}
	walked := redactWalk(v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(walked); err != nil {
		return "", false
	}
	return strings.TrimRight(buf.String(), "\n"), true
}

// maxRedactDepth limits structural recursion to prevent stack overflow on
// pathologically nested payloads.
const maxRedactDepth = 200

// redactWalk recursively walks a decoded JSON value and redacts secrets.
func redactWalk(v any) any { return redactWalkDepth(v, 0) }

func redactWalkDepth(v any, depth int) any {
	if depth > maxRedactDepth {
		return v
	}
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if isSecretKey(k) {
				x[k] = "[REDACTED:key]" // whole subtree, any type
			} else {
				x[k] = redactWalkDepth(val, depth+1)
			}
		}
		return x
	case []any:
		for i := range x {
			x[i] = redactWalkDepth(x[i], depth+1)
		}
		return x
	case string:
		return redactString(x) // recurse into JSON-in-string + regex
	default:
		return v // numbers (json.Number), bools, null
	}
}
