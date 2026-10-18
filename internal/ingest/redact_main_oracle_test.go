// Frozen copy of 165f184:internal/ingest/redact.go, used only to prove no
// redaction coverage regression. Only identifiers were renamed for isolation.
// Do not update this oracle along with production fixes. When coverage policy
// intentionally changes, explicitly review the removed-position differences,
// then refresh from the approved baseline (renaming identifiers only), or retire
// this oracle after equivalent independent coverage checks replace it.
package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// mainOracleRedactRule pairs a compiled pattern with the replacement applied to matches,
// plus a cheap necessary-condition prefilter. The prefilter never changes what
// is redacted: it only lets mainOracleRedact skip a regexp that provably cannot match.
//
//   - needles: the match must contain at least one of these literal substrings
//     (a regexp match that lacks them is impossible). nil means "no safe literal
//     feature": the rule always runs (the safe default).
//   - also: if non-empty, the match must additionally contain at least one of
//     these bytes (e.g. the ':' or '=' of KEY=value).
//   - foldCase: the pattern is (?i). needles must then be lower-case and are
//     matched against mainOracleFoldLower(text). Go's (?i) uses Unicode simple folding;
//     the only non-ASCII runes that fold onto an ASCII letter are U+212A KELVIN
//     SIGN (k) and U+017F LONG S (s) (TestSimpleFoldOnlyKAndS pins that).
//     strings.ToLower already maps U+212A to k, and mainOracleFoldLower additionally maps
//     U+017F to s, so no non-ASCII fallback is needed.
//
// mainOracleValidateRedactRules (run from mainOracleInit) panics on a table that breaks these
// constraints.
type mainOracleRedactRule struct {
	re       *regexp.Regexp
	repl     string
	needles  []string
	also     string
	foldCase bool
}

// mainOracleValidateRedactRules enforces the prefilter table's safety constraints:
// foldCase must agree with a leading (?i) in the pattern, and a foldCase rule's
// needles must be lower-case (they are compared with a lower-cased text).
func mainOracleValidateRedactRules(rules []mainOracleRedactRule) error {
	for i, r := range rules {
		ci := strings.HasPrefix(r.re.String(), "(?i)")
		if r.foldCase != ci {
			return fmt.Errorf("redact rule %d: foldCase=%v but pattern case-insensitive=%v (%s)", i, r.foldCase, ci, r.re)
		}
		if r.foldCase {
			for _, n := range r.needles {
				if n != strings.ToLower(n) {
					return fmt.Errorf("redact rule %d: needle %q must be lower-case for a foldCase rule", i, n)
				}
			}
		}
	}
	return nil
}

// mainOracleRedactRules is a conservative set: high-signal secret shapes only, to avoid
// eating legitimate text (false positives destroy searchability).
var mainOracleRedactRules = []mainOracleRedactRule{
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
	// left untouched). mainOracleRedact to end-of-line (HTTP-correct): security beats
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

// mainOraclePrivateOpenRe / mainOraclePrivateCloseRe delimit a user-marked <private>…</private>
// element. A self-closing <private/> is not an opener (it would otherwise
// swallow everything up to the next closing tag); the closing tag may carry
// trailing whitespace.
var (
	mainOraclePrivateOpenRe  = regexp.MustCompile(`(?i)<private(?:\s[^>]*[^/>])?\s*>`)
	mainOraclePrivateCloseRe = regexp.MustCompile(`(?i)</private\s*>`)
)

// mainOracleStripPrivate removes every <private> element from s, content included. It
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
func mainOracleStripPrivate(s string) string {
	if strings.IndexByte(s, '<') < 0 {
		return s // no tag at all: skip the scan on every plain message
	}
	var out strings.Builder
	pos := 0
	for {
		open := mainOraclePrivateOpenRe.FindStringIndex(s[pos:])
		if open == nil {
			break
		}
		start, i := pos+open[0], pos+open[1]
		depth := 1
		for depth > 0 {
			close := mainOraclePrivateCloseRe.FindStringIndex(s[i:])
			if close == nil {
				break // unterminated: nothing from `start` on is removed
			}
			if inner := mainOraclePrivateOpenRe.FindStringIndex(s[i : i+close[0]]); inner != nil {
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
	if err := mainOracleValidateRedactRules(mainOracleRedactRules); err != nil {
		panic(err)
	}
}

// mainOracleFoldLower lower-cases s the way Go's (?i) matching sees ASCII letters:
// strings.ToLower (which maps U+212A to k) plus U+017F to s. The result is only
// used for substring tests of lower-case ASCII needles; extra spurious letters
// it may produce merely let a regexp run.
func mainOracleFoldLower(s string) string {
	l := strings.ToLower(s) // no allocation when s has no upper-case letter
	if strings.Contains(l, "\u017f") {
		l = strings.ReplaceAll(l, "\u017f", "s")
	}
	return l
}

// mayMatch reports whether r could match s. It is a necessary condition only:
// false means the regexp provably cannot match (skip it), true means "run it".
// lower returns mainOracleFoldLower(s) on demand, so the copy is built only for a
// foldCase rule that got past the cheap checks.
func (r *mainOracleRedactRule) mayMatch(s string, lower func() string) bool {
	if r.needles == nil {
		return true
	}
	if r.also != "" && !strings.ContainsAny(s, r.also) {
		return false
	}
	hay := s
	if r.foldCase {
		hay = lower()
	}
	for _, n := range r.needles {
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

// mainOracleRedact replaces recognized secret patterns in s. It is intentionally
// conservative; see mainOracleRedactRules. Each rule's regexp only runs when its cheap
// necessary-condition prefilter passes; the output is identical to running
// every rule (mainOracleRedactUnfiltered, pinned by tests and a real-corpus comparison).
func mainOracleRedact(s string) string { return mainOracleRedactWith(s, true, nil) }

// mainOracleRedactWith is mainOracleRedact with the prefilter switchable (filtered=false runs every
// rule, the reference path). If ran is non-nil it receives the index of every
// rule whose regexp was executed, so tests can observe what was skipped.
func mainOracleRedactWith(s string, filtered bool, ran *[]int) string {
	return mainOracleApplyRules(mainOracleRedactRules, s, filtered, ran)
}

// mainOracleApplyRules is the rule loop behind mainOracleRedactWith, over an explicit rule slice so
// tests can drive it with their own tables.
func mainOracleApplyRules(rules []mainOracleRedactRule, s string, filtered bool, ran *[]int) string {
	var lower string
	haveLower := false
	getLower := func() string {
		if !haveLower {
			lower, haveLower = mainOracleFoldLower(s), true
		}
		return lower
	}
	for i := range rules {
		r := &rules[i]
		if filtered && !r.mayMatch(s, getLower) {
			continue
		}
		if ran != nil {
			*ran = append(*ran, i)
		}
		if ns := r.re.ReplaceAllString(s, r.repl); ns != s {
			s = ns
			haveLower = false // text changed: rebuild the folded copy on demand
		}
	}
	return s
}

// mainOracleRedactUnfiltered runs every rule on s with no prefilter. It is the reference
// implementation mainOracleRedact must match byte for byte.
func mainOracleRedactUnfiltered(s string) string {
	for _, r := range mainOracleRedactRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

// mainOracleSecretKeyRe matches secret-named keys with word boundaries to avoid false
// positives (e.g. "tokenizer", "author", "oauth_provider").
// Compound terms use spaces because mainOracleIsSecretKey normalizes underscores to spaces.
var mainOracleSecretKeyRe = regexp.MustCompile(`\b(password|passwd|secret|secrets|credential|credentials|token|apikey|api key|accesskey|access key|privatekey|private key|secret key|auth token|dsn|connection string|conn str)\b`)

// mainOracleSecretKeyExact are secret-bearing key names matched only as the WHOLE normalized
// key (after underscore/hyphen/camelCase splitting), not as a substring — so
// "authorization"/"cookie"/"set cookie" are caught but "authorizationURL",
// "cookieJar", and "setCookieBanner" are not.
var mainOracleSecretKeyExact = map[string]bool{
	"authorization": true, "proxy authorization": true,
	"cookie": true, "set cookie": true,
}

// mainOracleCamelBoundaryRe matches the boundary between a lower/digit and an upper char
// so "secretKey" → "secret Key" and "dbPassword" → "db Password".
var mainOracleCamelBoundaryRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// mainOracleIsSecretKey returns true if the key name looks like a secret holder.
// Underscores and hyphens are treated as word separators; camelCase is split
// so that "db_password", "dbPassword", and "api-key" all match.
func mainOracleIsSecretKey(k string) bool {
	n := mainOracleCamelBoundaryRe.ReplaceAllString(k, "$1 $2")
	n = strings.NewReplacer("_", " ", "-", " ").Replace(n)
	n = strings.ToLower(n)
	if mainOracleSecretKeyExact[n] {
		return true
	}
	return mainOracleSecretKeyRe.MatchString(n)
}

// mainOracleRedactString is the single text redactor used for message content, session
// titles, and string leaves inside JSON.
//
// It structurally redacts any JSON object/array embedded ANYWHERE in the string
// (so a secret under a generic key like {"apiKey":"…"} is caught even amid
// surrounding prose), and applies the shape regexes to the non-JSON text around
// those spans. Free-text secrets with no recognizable shape are best-effort.
func mainOracleRedactString(s string) string { return mainOracleRedactStringWith(s, true) }

// mainOracleRedactStringWith is mainOracleRedactString with the regex prefilter switchable, so tests
// can compare the filtered and unfiltered paths through the whole JSON walk.
func mainOracleRedactStringWith(s string, filtered bool) string {
	// Strip <private> first, while the string is still whole: the JSON split
	// below would otherwise hand a private block containing JSON to the regex
	// in pieces that never match.
	s = mainOracleStripPrivate(s)
	if !strings.ContainsAny(s, "{[") {
		return mainOracleRedactWith(s, filtered, nil) // fast path: no JSON to find
	}
	var out strings.Builder
	textStart := 0
	for i := 0; i < len(s); {
		if s[i] == '{' || s[i] == '[' {
			if red, n, ok := mainOracleRedactJSONAt(s[i:], filtered); ok {
				out.WriteString(mainOracleRedactWith(s[textStart:i], filtered, nil)) // regex-redact preceding prose
				out.WriteString(red)                                                 // structurally-redacted JSON span
				i += n
				textStart = i
				continue
			}
		}
		i++
	}
	out.WriteString(mainOracleRedactWith(s[textStart:], filtered, nil))
	return out.String()
}

// mainOracleRedactMemo remembers the last mainOracleRedactString result. One tool call redacts the
// same input string for its summary and again for its activity target; sharing
// the memo makes that one pass with a byte-identical result (mainOracleRedactString is a
// pure function of its input). A nil *mainOracleRedactMemo just calls mainOracleRedactString.
// debt: the third pass over the same string (the raw_json walk in mainOracleRedactJSON)
// and the final pass over name+" "+summary are not shared: redaction is not
// idempotent in general (TestToolUseContentKeepsSecondRedactionPass), so only
// passes over the identical input can be merged without changing output.
//
// debt: 工具輸入 JSON 仍被 summary 與 target 各解析一次；在呼叫點解析一次並把選定值傳給兩邊即可移除 memo
type mainOracleRedactMemo struct {
	in, out string
	set     bool
}

func (m *mainOracleRedactMemo) redact(s string) string {
	if m == nil {
		return mainOracleRedactString(s)
	}
	if m.set && m.in == s {
		return m.out
	}
	m.in, m.out, m.set = s, mainOracleRedactString(s), true
	return m.out
}

// mainOracleRedactJSONAt tries to decode a single JSON object or array at the start of t.
// On success it returns the structurally-redacted JSON and the bytes consumed.
// Bare scalars are NOT treated as embedded JSON, so a stray '{'/'[' in prose
// (or things like "{1,2}" which are not valid JSON) do not trigger.
func mainOracleRedactJSONAt(t string, filtered bool) (string, int, bool) {
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
	if err := enc.Encode(mainOracleRedactWalkDepth(v, 0, filtered)); err != nil {
		return "", 0, false
	}
	return strings.TrimRight(buf.String(), "\n"), int(dec.InputOffset()), true
}

// mainOracleRedactJSON walks a raw JSON event line and redacts secret values structurally.
// On decode failure it falls back to text mainOracleRedact.
func mainOracleRedactJSON(line []byte) []byte {
	if result, ok := mainOracleRedactJSONValue(line); ok {
		return []byte(result)
	}
	// This is the one persisted-text path that bypasses mainOracleRedactString, so it
	// strips <private> itself to keep the invariant.
	return []byte(mainOracleRedact(mainOracleStripPrivate(string(line))))
}

// mainOracleRedactJSONValue decodes b as JSON, walks it with mainOracleRedactWalk, and re-encodes
// it. Returns ("", false) if b is not valid JSON.
func mainOracleRedactJSONValue(b []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	// Require EOF: trailing non-whitespace bytes mean this is not pure JSON.
	// Fall back to text mainOracleRedact so trailing bytes are not silently dropped.
	if _, err := dec.Token(); err != io.EOF {
		return "", false
	}
	walked := mainOracleRedactWalk(v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(walked); err != nil {
		return "", false
	}
	return strings.TrimRight(buf.String(), "\n"), true
}

// mainOracleMaxRedactDepth limits structural recursion to prevent stack overflow on
// pathologically nested payloads.
const mainOracleMaxRedactDepth = 200

// mainOracleRedactWalk recursively walks a decoded JSON value and redacts secrets.
func mainOracleRedactWalk(v any) any { return mainOracleRedactWalkDepth(v, 0, true) }

func mainOracleRedactWalkDepth(v any, depth int, filtered bool) any {
	if depth > mainOracleMaxRedactDepth {
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
			if mainOracleIsSecretKey(k) { // judged on the original key name
				val = "[REDACTED:key]" // whole subtree, any type
			} else {
				val = mainOracleRedactWalkDepth(x[k], depth+1, filtered)
			}
			nk := mainOracleRedactStringWith(k, filtered)
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
			x[i] = mainOracleRedactWalkDepth(x[i], depth+1, filtered)
		}
		return x
	case string:
		return mainOracleRedactStringWith(x, filtered) // recurse into JSON-in-string + regex
	default:
		return v // numbers (json.Number), bools, null
	}
}
