package ingest

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strings"
)

// redactRule pairs a compiled pattern with the replacement applied to matches,
// plus a cheap necessary-condition prefilter. The prefilter never changes what
// is redacted: it only lets Redact skip a regexp that provably cannot match.
//
//   - needles: the match must contain at least one of these literal substrings
//     (a regexp match that lacks them is impossible). nil means "no safe literal
//     feature", which requires runAlways to be set: such a rule always runs.
//   - also: if non-empty, the match must additionally contain at least one of
//     these bytes (e.g. the ':' or '=' of KEY=value).
//   - foldCase: the pattern is (?i). needles are then lower-case and matched
//     against a lower-cased copy of the text. Go's (?i) uses Unicode simple
//     folding (U+212A KELVIN SIGN matches k, U+017F LONG S matches s), so when a
//     needle contains k or s and the text has any non-ASCII byte, the rule runs
//     unconditionally (those are the only ASCII letters with non-ASCII fold
//     partners; TestSimpleFoldOnlyKAndS pins that). Needles without k/s, and
//     needles that are not letters, are unaffected by the non-ASCII fold.
//   - foldFallback: derived from the above at init; not set by hand.
type redactRule struct {
	re           *regexp.Regexp
	repl         string
	needles      []string
	also         string
	foldCase     bool
	runAlways    bool
	foldFallback bool
}

// redactRules is a conservative set: high-signal secret shapes only, to avoid
// eating legitimate text (false positives destroy searchability).
var redactRules = []redactRule{
	// PEM private key blocks (multi-line).
	// prefilter: the pattern spells the literal "PRIVATE KEY" (case-sensitive) in both fences.
	{re: regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), repl: "[REDACTED:private-key]", needles: []string{"PRIVATE KEY"}},
	// AWS access key id.
	// prefilter: the match starts with the literal "AKIA".
	{re: regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), repl: "[REDACTED:aws-key]", needles: []string{"AKIA"}},
	// Google API key.
	// prefilter: the match starts with the literal "AIza".
	{re: regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`), repl: "[REDACTED:gcp-key]", needles: []string{"AIza"}},
	// GitHub tokens (classic + fine-grained + app).
	// prefilter: gh[pousr]_ expands to exactly these five literals.
	{re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`), repl: "[REDACTED:github-token]", needles: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}},
	// prefilter: the match starts with the literal "github_pat_".
	{re: regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`), repl: "[REDACTED:github-token]", needles: []string{"github_pat_"}},
	// Slack tokens.
	// prefilter: the match starts with the literal "xox" (then [baprs]-).
	{re: regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`), repl: "[REDACTED:slack-token]", needles: []string{"xox"}},
	// OpenAI / Anthropic style keys.
	// prefilter: the match starts with the literal "sk-".
	{re: regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{20,}\b`), repl: "[REDACTED:api-key]", needles: []string{"sk-"}},
	// Stripe keys (underscore form: sk_live_, pk_live_, rk_live_, _test_).
	// prefilter: [rsp]k_(?:live|test)_ always contains k_live_ or k_test_.
	{re: regexp.MustCompile(`\b[rsp]k_(?:live|test)_[A-Za-z0-9]{10,}\b`), repl: "[REDACTED:stripe-key]", needles: []string{"k_live_", "k_test_"}},
	// JWTs (header.payload.signature, base64url).
	// prefilter: the match starts with the literal "eyJ".
	{re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), repl: "[REDACTED:jwt]", needles: []string{"eyJ"}},
	// Bearer tokens in headers.
	// prefilter: (?i) "Bearer" is in every match; compared against lower-cased ASCII text.
	{re: regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._\-]{12,}`), repl: "Bearer [REDACTED:token]", needles: []string{"bearer"}, foldCase: true},
	// Basic auth credentials, anchored on the explicit (Proxy-)Authorization header
	// so the English word "basic" in prose is never matched (base64 of user:pass can
	// be all letters, so a bare "Basic <word>" rule would eat phrases like "basic
	// auth"). No length floor: the header shape is already high-signal and short
	// credentials (e.g. "a:b") must not leak. The capture keeps the header name.
	// prefilter: every match contains "Authorization:" contiguously (the optional
	//            Proxy- prefix sits before it); (?i), lower-cased ASCII text.
	{re: regexp.MustCompile(`(?i)((?:Proxy-)?Authorization:\s*Basic\s+)[A-Za-z0-9+/=]+`), repl: "${1}[REDACTED:token]", needles: []string{"authorization:"}, foldCase: true},
	// Cookie / Set-Cookie header values (session ids, CSRF tokens). Anchored on the
	// header colon form AND requiring a name=value (so prose like "cookie: enabled" is
	// left untouched). Redact to end-of-line (HTTP-correct): security beats
	// searchability, so a quoted value like session="secret" is never left exposed even
	// though same-line trailing content is also redacted.
	// prefilter: every match contains "Cookie:" contiguously; (?i), lower-cased ASCII text.
	{re: regexp.MustCompile(`(?i)((?:Set-)?Cookie:\s*)[^\r\n]*=[^\r\n]*`), repl: "${1}[REDACTED:cookie]", needles: []string{"cookie:"}, foldCase: true},
	// KEY=value / KEY: value where KEY names a secret. Value redacted, key kept.
	// prefilter: the keyword group is mandatory and each alternative contains one of
	//            these (API[_-]?KEY etc. all contain "key"), and the match needs a
	//            [:=] separator; (?i), lower-cased ASCII text.
	{re: regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:SECRET|PASSWORD|PASSWD|API[_-]?KEY|ACCESS[_-]?KEY|PRIVATE[_-]?KEY|TOKEN|CREDENTIAL)[A-Z0-9_]*)\s*([:=])\s*["']?[^\s"']{6,}["']?`), repl: `$1$2[REDACTED:secret]`, needles: []string{"secret", "password", "passwd", "key", "token", "credential"}, also: ":=", foldCase: true},
	// Connection strings: scheme://user:pass@host (requires user:pass@ authority).
	// Stop at common delimiters so trailing punctuation like ),;> is preserved.
	// prefilter: the match spells "://" and the user:pass@ authority needs an "@".
	{re: regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]*://[^\s:/@]+:[^\s:/@]+@[^\s'")>\]},;]+`), repl: "[REDACTED:connstring]", needles: []string{"://"}, also: "@", foldCase: true},
}

// privateOpenRe / privateCloseRe delimit a user-marked <private>…</private>
// element. A self-closing <private/> is not an opener (it would otherwise
// swallow everything up to the next closing tag); the closing tag may carry
// trailing whitespace.
var (
	privateOpenRe  = regexp.MustCompile(`(?i)<private(?:\s[^>]*[^/>])?\s*>`)
	privateCloseRe = regexp.MustCompile(`(?i)</private\s*>`)
)

// stripPrivate removes every <private> element from s, content included. It
// tracks nesting depth so an inner <private> never ends the outer element
// early (a non-greedy regex would leave the outer tail indexed — caught by the
// final whole-change verifier). Two sibling blocks remove exactly two spans;
// an unterminated opener leaves the text from that opener on intact. It runs
// before secret redaction so a secret inside a private block vanishes without
// leaving a [REDACTED:…] marker that would reveal one was there. The scan is
// block-local by construction: it only ever sees one string.
// debt: with thousands of NESTED openers before one closer the inner loop
// rescans to that closer once per opener (measured 40k nested → ~2.6 s);
// unclosed or sibling blocks stay linear. If a transcript ever trips this,
// pre-index closer positions instead of re-searching.
func stripPrivate(s string) string {
	if strings.IndexByte(s, '<') < 0 {
		return s // no tag at all: skip the scan on every plain message
	}
	var out strings.Builder
	pos := 0
	for {
		open := privateOpenRe.FindStringIndex(s[pos:])
		if open == nil {
			break
		}
		start, i := pos+open[0], pos+open[1]
		depth := 1
		for depth > 0 {
			close := privateCloseRe.FindStringIndex(s[i:])
			if close == nil {
				break // unterminated: nothing from `start` on is removed
			}
			if inner := privateOpenRe.FindStringIndex(s[i : i+close[0]]); inner != nil {
				depth++
				i += inner[1]
				continue
			}
			depth--
			i += close[1]
		}
		if depth > 0 {
			// Unterminated: this opener is literal text. Keep it and resume
			// the scan right after it, so a later complete element is still
			// removed (a stray "<private>" earlier in a message must not
			// shield the real one that follows — found on the live index).
			out.WriteString(s[pos : start+(open[1]-open[0])])
			pos = start + (open[1] - open[0])
			continue
		}
		out.WriteString(s[pos:start])
		pos = i
	}
	out.WriteString(s[pos:])
	return out.String()
}

func init() {
	for i := range redactRules {
		r := &redactRules[i]
		if r.foldCase {
			for _, n := range r.needles {
				if strings.ContainsAny(n, "ks") {
					r.foldFallback = true
				}
			}
		}
	}
}

// prefilterOff makes Redact run every rule unconditionally. Tests flip it to
// compare the filtered and unfiltered paths; production never sets it.
var prefilterOff bool

// mayMatch reports whether r could match s. It is a necessary condition only:
// false means the regexp provably cannot match (skip it), true means "run it".
// lower is strings.ToLower(s) (a superset test for ASCII needles: lower-casing
// never removes an ASCII letter run, and may only add spurious ones, which merely
// run the regexp); ascii says s is pure ASCII.
func (r *redactRule) mayMatch(s, lower string, ascii bool) bool {
	if r.runAlways {
		return true
	}
	hay := s
	if r.foldCase {
		if !ascii && r.foldFallback {
			return true
		}
		hay = lower
	}
	found := false
	for _, n := range r.needles {
		if strings.Contains(hay, n) {
			found = true
			break
		}
	}
	return found && (r.also == "" || strings.ContainsAny(s, r.also))
}

// Redact replaces recognized secret patterns in s. It is intentionally
// conservative; see redactRules. Each rule's regexp only runs when its cheap
// necessary-condition prefilter passes; the output is identical to running
// every rule (redactUnfiltered, pinned by tests and a real-corpus comparison).
func Redact(s string) string {
	if prefilterOff {
		return redactUnfiltered(s)
	}
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	lower := strings.ToLower(s) // no allocation when s has no upper-case letter
	for i := range redactRules {
		r := &redactRules[i]
		if r.mayMatch(s, lower, ascii) {
			if ns := r.re.ReplaceAllString(s, r.repl); ns != s {
				s = ns
				lower = strings.ToLower(s) // text changed: refresh the folded copy
			}
		}
	}
	return s
}

// redactUnfiltered runs every rule on s with no prefilter. It is the reference
// implementation Redact must match byte for byte.
func redactUnfiltered(s string) string {
	for _, r := range redactRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

// secretKeyRe matches secret-named keys with word boundaries to avoid false
// positives (e.g. "tokenizer", "author", "oauth_provider").
// Compound terms use spaces because isSecretKey normalizes underscores to spaces.
var secretKeyRe = regexp.MustCompile(`\b(password|passwd|secret|secrets|credential|credentials|token|apikey|api key|accesskey|access key|privatekey|private key|secret key|auth token|dsn|connection string|conn str)\b`)

// secretKeyExact are secret-bearing key names matched only as the WHOLE normalized
// key (after underscore/hyphen/camelCase splitting), not as a substring — so
// "authorization"/"cookie"/"set cookie" are caught but "authorizationURL",
// "cookieJar", and "setCookieBanner" are not.
var secretKeyExact = map[string]bool{
	"authorization": true, "proxy authorization": true,
	"cookie": true, "set cookie": true,
}

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
	if secretKeyExact[n] {
		return true
	}
	return secretKeyRe.MatchString(n)
}

// redactString is the single text redactor used for message content, session
// titles, and string leaves inside JSON.
//
// It structurally redacts any JSON object/array embedded ANYWHERE in the string
// (so a secret under a generic key like {"apiKey":"…"} is caught even amid
// surrounding prose), and applies the shape regexes to the non-JSON text around
// those spans. Free-text secrets with no recognizable shape are best-effort.
func redactString(s string) string {
	// Strip <private> first, while the string is still whole: the JSON split
	// below would otherwise hand a private block containing JSON to the regex
	// in pieces that never match.
	s = stripPrivate(s)
	if !strings.ContainsAny(s, "{[") {
		return Redact(s) // fast path: no JSON to find
	}
	var out strings.Builder
	textStart := 0
	for i := 0; i < len(s); {
		if s[i] == '{' || s[i] == '[' {
			if red, n, ok := redactJSONAt(s[i:]); ok {
				out.WriteString(Redact(s[textStart:i])) // regex-redact preceding prose
				out.WriteString(red)                    // structurally-redacted JSON span
				i += n
				textStart = i
				continue
			}
		}
		i++
	}
	out.WriteString(Redact(s[textStart:]))
	return out.String()
}

// redactMemo remembers the last redactString result. One tool call redacts the
// same input string for its summary and again for its activity target; sharing
// the memo makes that one pass with a byte-identical result (redactString is a
// pure function of its input). A nil *redactMemo just calls redactString.
// debt: the third pass over the same string (the raw_json walk in redactJSON)
// and the final pass over name+" "+summary are not shared: redaction is not
// idempotent in general (TestRedactNotIdempotentOnAdversarialText), so only
// passes over the identical input can be merged without changing output.
type redactMemo struct {
	in, out string
	set     bool
}

func (m *redactMemo) redact(s string) string {
	if m == nil {
		return redactString(s)
	}
	if m.set && m.in == s {
		return m.out
	}
	m.in, m.out, m.set = s, redactString(s), true
	return m.out
}

// redactJSONAt tries to decode a single JSON object or array at the start of t.
// On success it returns the structurally-redacted JSON and the bytes consumed.
// Bare scalars are NOT treated as embedded JSON, so a stray '{'/'[' in prose
// (or things like "{1,2}" which are not valid JSON) do not trigger.
func redactJSONAt(t string) (string, int, bool) {
	dec := json.NewDecoder(strings.NewReader(t))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", 0, false
	}
	switch v.(type) {
	case map[string]any, []any:
	default:
		return "", 0, false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(redactWalk(v)); err != nil {
		return "", 0, false
	}
	return strings.TrimRight(buf.String(), "\n"), int(dec.InputOffset()), true
}

// redactJSON walks a raw JSON event line and redacts secret values structurally.
// On decode failure it falls back to text Redact.
func redactJSON(line []byte) []byte {
	if result, ok := redactJSONValue(line); ok {
		return []byte(result)
	}
	// This is the one persisted-text path that bypasses redactString, so it
	// strips <private> itself to keep the invariant.
	return []byte(Redact(stripPrivate(string(line))))
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
		return "[REDACTED:depth]" // fail closed: never pass unredacted deep content through
	}
	switch x := v.(type) {
	case map[string]any:
		// Keys are strings too: a token or a <private> element used as a
		// key must not reach the index. Build a fresh map (never edit x while
		// ranging it) and sort so a rename collision resolves deterministically.
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(x))
		for _, k := range keys {
			var val any
			if isSecretKey(k) { // judged on the original key name
				val = "[REDACTED:key]" // whole subtree, any type
			} else {
				val = redactWalkDepth(x[k], depth+1)
			}
			nk := redactString(k)
			if _, dup := out[nk]; dup {
				// Two keys redacted to the same name: keeping either value would
				// pick one arbitrarily and could surface the other's content
				// under a name it never had, so collapse to the marker.
				val = "[REDACTED:key]"
			}
			out[nk] = val
		}
		return out
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
