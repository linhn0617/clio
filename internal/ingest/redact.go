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

// redactRule pairs a compiled pattern with the replacement applied to matches,
// plus a cheap necessary-condition prefilter. The prefilter never changes what
// is redacted: it only lets Redact skip a regexp that provably cannot match.
//
//   - needles: the match must contain at least one of these literal substrings
//     (a regexp match that lacks them is impossible). nil means "no safe literal
//     feature": the rule always runs (the safe default).
//   - also: if non-empty, the match must additionally contain at least one of
//     these bytes (e.g. the ':' or '=' of KEY=value).
//   - foldCase: the pattern is (?i). needles must then be lower-case and are
//     matched against foldLower(text). Go's (?i) uses Unicode simple folding;
//     the only non-ASCII runes that fold onto an ASCII letter are U+212A KELVIN
//     SIGN (k) and U+017F LONG S (s) (TestSimpleFoldOnlyKAndS pins that).
//     strings.ToLower already maps U+212A to k, and foldLower additionally maps
//     U+017F to s, so no non-ASCII fallback is needed.
//
// validateRedactRules (run from init) panics on a table that breaks these
// constraints.
type redactRule struct {
	re        *regexp.Regexp
	repl      string
	needles   []string
	also      string
	foldCase  bool
	keepMatch func(string) bool // explicit false-positive guards; true preserves the match
}

// validateRedactRules enforces the prefilter table's safety constraints:
// foldCase must agree with a leading (?i) in the pattern, and a foldCase rule's
// needles must be lower-case (they are compared with a lower-cased text).
func validateRedactRules(rules []redactRule) error {
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

// redactRules is a conservative set: high-signal secret shapes only, to avoid
// eating legitimate text (false positives destroy searchability). Connection
// strings run last, as on main: earlier key/header rules must see query keys and
// adjacent headers before a URL replacement can consume their names.
//
// debt: four secret shapes are deliberately NOT matched, because any regexp for
// them either over-redacts prose or needs a real tokenizer. Each is "add it if
// a real leaked case shows up in the index":
//   - a quoted password containing spaces (`password="a b c"`): only the first
//     word is redacted by the KEY=value rule;
//   - `curl -u user:pass` / `--user user:pass` (a bare user:pass argument);
//   - `.netrc` lines (`machine h login u password p`);
//   - docker `config.json` `auths` blocks (base64 `auth` values under a registry key).
//
// debt: handle PEM/PGP blocks without an END line if a truncated private key is ever found in the index.
var redactRules = []redactRule{
	// PEM private key blocks (multi-line).
	// prefilter: the pattern spells the literal "PRIVATE KEY" (case-sensitive) in both fences.
	{re: regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), repl: "[REDACTED:private-key]", needles: []string{"PRIVATE KEY"}},
	// PGP private key block (its fence ends "PRIVATE KEY BLOCK-----", which the PEM
	// rule above does not match). Public keys and messages use other fences.
	// prefilter: the pattern spells the literal "PGP PRIVATE KEY BLOCK" in both fences.
	{re: regexp.MustCompile(`(?s)-----BEGIN PGP PRIVATE KEY BLOCK-----.*?-----END PGP PRIVATE KEY BLOCK-----`), repl: "[REDACTED:private-key]", needles: []string{"PGP PRIVATE KEY BLOCK"}},
	// AWS access key id.
	// A fixed-length id may be immediately followed by a secret key name;
	// requiring a trailing word boundary would leak AKIA…secret=….
	// prefilter: the match starts with the literal "AKIA".
	{re: regexp.MustCompile(`\bAKIA[0-9A-Z]{16}`), repl: "[REDACTED:aws-key]", needles: []string{"AKIA"}},
	// Google API key.
	// prefilter: the match starts with the literal "AIza".
	{re: regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`), repl: "[REDACTED:gcp-key]", needles: []string{"AIza"}},
	// GitHub tokens (classic + fine-grained + app).
	// prefilter: gh[pousr]_ expands to exactly these five literals.
	{re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`), repl: "[REDACTED:github-token]", needles: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}},
	// prefilter: the match starts with the literal "github_pat_".
	{re: regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`), repl: "[REDACTED:github-token]", needles: []string{"github_pat_"}},
	// GitLab personal/project/group access tokens (glpat- + 20 chars).
	// prefilter: the match starts with the literal "glpat-".
	{re: regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}`), repl: "[REDACTED:gitlab-token]", needles: []string{"glpat-"}, keepMatch: tokenPlaceholder},
	// npm automation/publish tokens (npm_ + 36 alphanumerics). Alphanumerics only, so
	// env-var names like npm_config_registry never match.
	// prefilter: the match starts with the literal "npm_".
	{re: regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36,}\b`), repl: "[REDACTED:npm-token]", needles: []string{"npm_"}},
	// Hugging Face user access tokens (hf_ + 34 alphanumerics). No underscore in the
	// body, so identifiers such as hf_hub_download never match.
	// prefilter: the match starts with the literal "hf_".
	{re: regexp.MustCompile(`(?:^|([^A-Za-z0-9_.]))hf_[A-Za-z0-9]{34,}\b`), repl: "${1}[REDACTED:hf-token]", needles: []string{"hf_"}},
	// Slack tokens.
	// prefilter: the match starts with the literal "xox" (then [baprs]-).
	{re: regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`), repl: "[REDACTED:slack-token]", needles: []string{"xox"}},
	// Slack incoming-webhook URLs: the T…/B…/secret path is the credential. The
	// 16-char floor excludes short examples; the placeholder guard also excludes
	// Slack's official 24-X example.
	// prefilter: every match spells the literal host+path "hooks.slack.com/services/".
	{re: regexp.MustCompile(`(?i)\b(?:https?://)?hooks\.slack\.com/services/T[A-Za-z0-9]+/B[A-Za-z0-9]+/[A-Za-z0-9]{16,}`), repl: "[REDACTED:slack-webhook]", needles: []string{"hooks.slack.com/services/"}, foldCase: true, keepMatch: tokenPlaceholder},
	// OpenAI / Anthropic style keys. An underscore directly before sk- counts as a
	// boundary (`x_sk-…` is a prefix_secret concatenation; \b does not hold between
	// two word characters); letters and digits before it stay word material so
	// "risk-assessment-…" / "disk-usage-…" are not eaten. The captured underscore is kept.
	// prefilter: the match starts with the literal "sk-".
	{re: regexp.MustCompile(`(?:\b|(_))sk-[A-Za-z0-9_\-]{20,}\b`), repl: "${1}[REDACTED:api-key]", needles: []string{"sk-"}, keepMatch: underscoreSKWord},
	// Stripe keys (underscore form: sk_live_, pk_live_, rk_live_, _test_).
	// prefilter: [rsp]k_(?:live|test)_ always contains k_live_ or k_test_.
	{re: regexp.MustCompile(`\b[rsp]k_(?:live|test)_[A-Za-z0-9]{10,}\b`), repl: "[REDACTED:stripe-key]", needles: []string{"k_live_", "k_test_"}},
	// Stripe webhook signing secrets.
	// prefilter: the match starts with the literal "whsec_".
	{re: regexp.MustCompile(`\bwhsec_[A-Za-z0-9+/=]{16,}`), repl: "[REDACTED:stripe-webhook]", needles: []string{"whsec_"}},
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
	// GitHub's legacy `Authorization: token <x>` scheme. A 20-char floor keeps prose
	// such as "Authorization: token required for this endpoint" intact (real values
	// are 40 hex chars or a prefixed token). The capture keeps the header name.
	// prefilter: as the Basic rule, every match contains "Authorization:".
	{re: regexp.MustCompile(`(?i)((?:Proxy-)?Authorization:\s*token\s+)[A-Za-z0-9._\-]{20,}`), repl: "${1}[REDACTED:token]", needles: []string{"authorization:"}, foldCase: true, keepMatch: tokenPlaceholder},
	// Cookie / Set-Cookie header values (session ids, CSRF tokens). Anchored on the
	// header colon form AND requiring a name=value (so prose like "cookie: enabled" is
	// left untouched). Redact to end-of-line (HTTP-correct): security beats
	// searchability, so a quoted value like session="secret" is never left exposed even
	// though same-line trailing content is also redacted.
	// prefilter: every match contains "Cookie:" contiguously; (?i), lower-cased ASCII text.
	{re: regexp.MustCompile(`(?i)((?:Set-)?Cookie:\s*)[^\r\n]*=[^\r\n]*`), repl: "${1}[REDACTED:cookie]", needles: []string{"cookie:"}, foldCase: true},
	// KEY=value / KEY: value where KEY names a secret. Value redacted, key kept.
	// _PASS is limited to credential contexts: FIRST_PASS / RENDER_PASS are
	// ordinary computation stages even with long values or uppercase spelling.
	// PASSPHRASE and suffixed _PWD are credentials; bare PWD is the shell cwd.
	// The start boundary is `\b`, or — where \b cannot hold because the key begins
	// with U+017F LONG S / U+212A KELVIN SIGN (non-ASCII, so not a Go word char, yet
	// (?i) folds them onto s / k) — a consumed non-word char or the text start. The
	// consumed char is captured and re-emitted; `\b` stays first so a quote-closed
	// value directly followed by the next key (A='x'B_TOKEN=…) still matches B_TOKEN.
	// prefilter: the keyword group is mandatory and each alternative contains one of
	//            these (API[_-]?KEY etc. all contain "key"; _PASS, _PWD and PASSPHRASE have
	//            their own), and the match needs a [:=] separator; (?i), lower-cased ASCII text.
	{re: regexp.MustCompile(`(?i)(?:\b|(^|[^A-Z0-9_]))((?:[A-Z0-9_]*(?:SECRET|PASSWORD|PASSWD|PASSPHRASE|API[_-]?KEY|ACCESS[_-]?KEY|PRIVATE[_-]?KEY|TOKEN|CREDENTIAL)[A-Z0-9_]*)|(?:DB|MYSQL|SMTP|REDIS|ADMIN|ROOT|USER|MAIL|FTP|SSH|LDAP|PG|POSTGRES)_PASS|[A-Z0-9_]+_PWD)\s*([:=])\s*["']?[^\s"']{6,}["']?`), repl: `${1}${2}${3}[REDACTED:secret]`, needles: []string{"secret", "password", "passwd", "key", "token", "credential", "_pass", "_pwd", "passphrase"}, also: ":=", foldCase: true},
	// Connection strings run after the original token/header/key rules. Marker
	// alternatives consume an earlier replacement whole (including its ']'),
	// preserving coverage and producing one marker without a stray closing ']'.
	// Passwords may contain '/' or ':' and extend to the last '@'.
	// prefilter: every candidate contains both :// and @.
	{re: regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]*://(?:\[REDACTED:[a-z-]+\]|[^\s:/@])*:(?:\[REDACTED:[a-z-]+\]|[^\s'">])+@(?:\[REDACTED:[a-z-]+\]|[^\s'")>\]},;])+`), repl: "[REDACTED:connstring]", needles: []string{"://"}, also: "@", foldCase: true, keepMatch: connectionPortPath},
}

// connectionPortPath preserves URLs whose apparent password is a port followed
// by a path containing '@'. Main excluded '/' from passwords and kept these
// URLs intact. Restrict this guard to host-like users; admin:ab/cd@host remains
// credentialed. Port-only forms are a separate main-coverage policy decision.
func connectionPortPath(s string) bool {
	_, authority, _ := strings.Cut(s, "://")
	at := strings.LastIndexByte(authority, '@')
	if at < 0 {
		return false
	}
	user, password, ok := strings.Cut(authority[:at], ":")
	if !ok || !(user == "" || strings.EqualFold(user, "localhost") || strings.Contains(user, ".")) {
		return false
	}
	port, _, path := strings.Cut(password, "/")
	if !path || port == "" || strings.Contains(password, "://") {
		return false
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// tokenPlaceholder excludes recognizable documentation values from new rules.
// These rules did not exist on main; existing main token coverage is retained.
func tokenPlaceholder(s string) bool {
	u := strings.ToUpper(s)
	if strings.Contains(u, "YOUR_") || strings.Contains(u, "PLACEHOLDER") {
		return true
	}
	body := s
	if i := strings.LastIndexAny(body, "/-"); i >= 0 {
		body = body[i+1:]
	}
	return len(body) > 0 && strings.Trim(body, "xX") == ""
}

func underscoreSKWord(s string) bool {
	if !strings.HasPrefix(s, "_") {
		return false
	} // preserve main's standalone sk- coverage
	body := strings.TrimPrefix(s, "_sk-")
	if strings.HasPrefix(body, "proj-") || strings.HasPrefix(body, "ant-") {
		return false
	}
	// A lowercase word before a separator is still prose even when a later
	// segment contains a year or an uppercase identifier.
	if end := strings.IndexAny(body, "-_"); end >= 0 {
		body = body[:end]
	}
	return !strings.ContainsAny(body, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
}

func (r *redactRule) replace(s string) string {
	if r.keepMatch == nil {
		return r.re.ReplaceAllString(s, r.repl)
	}
	return r.re.ReplaceAllStringFunc(s, func(match string) string {
		if r.keepMatch(match) {
			return match
		}
		return r.re.ReplaceAllString(match, r.repl)
	})
}

// privateTag is one <private …> opener or </private …> closer: [start, end) in
// the scanned string. A user-marked <private>…</private> element is delimited
// by these. A self-closing <private/> is not an opener (it would otherwise
// swallow everything up to the next closing tag); the closing tag may carry
// trailing whitespace. Both tags end at the first '>' after their name, so a
// tag's end is a pure function of its start.
type privateTag struct{ start, end int }

// asciiSpace is the regexp \s class (RE2: [\t\n\f\r ], no \v).
func asciiSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

// hasPrefixFoldASCII reports whether s starts with the lower-case ASCII prefix p,
// ignoring ASCII case. "private" has no non-ASCII simple-fold partner (only k and
// s do; TestSimpleFoldOnlyKAndS pins that), so byte folding equals (?i).
func hasPrefixFoldASCII(s, p string) bool {
	if len(s) < len(p) {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != p[i] {
			return false
		}
	}
	return true
}

// scanPrivateTags lists, in start order, every valid opener and closer in s in
// one linear pass. The shapes are exactly those of the regexps
// (?i)<private(?:\s[^>]*[^/>])?\s*> and (?i)</private\s*>, which this replaces:
//   - an opener's body (between "<private" and the first '>') is empty, or starts
//     with a space char and does not end in '/' (so <private a/> is self-closing
//     but <private a/ > is an opener);
//   - a closer's body is whitespace only.
//
// Candidates may overlap (a "<private" can sit inside an earlier opener's body),
// so the scan resumes at the next '<', not past the tag.
func scanPrivateTags(s string) (openers, closers []privateTag) {
	// firstGT(from) is the first '>' at or after from, or len(s) if none. Candidates
	// arrive in increasing order, so one cached hit serves every candidate before
	// it and each byte is searched at most once (a '<private' flood with no '>'
	// must not rescan the tail per candidate).
	nextGT := -1
	firstGT := func(from int) int {
		if nextGT >= from {
			return nextGT
		}
		if i := strings.IndexByte(s[from:], '>'); i >= 0 {
			nextGT = from + i
		} else {
			nextGT = len(s)
		}
		return nextGT
	}
	for i := 0; i < len(s); i++ {
		p := strings.IndexByte(s[i:], '<')
		if p < 0 {
			break
		}
		i += p
		switch {
		case hasPrefixFoldASCII(s[i:], "<private"):
			gt := firstGT(i + len("<private"))
			if gt >= len(s) {
				continue
			}
			body := s[i+len("<private") : gt]
			if len(body) == 0 || asciiSpace(body[0]) && body[len(body)-1] != '/' {
				openers = append(openers, privateTag{i, gt + 1})
			}
		case hasPrefixFoldASCII(s[i:], "</private"):
			gt := firstGT(i + len("</private"))
			if gt >= len(s) {
				continue
			}
			ws := true
			for _, c := range []byte(s[i+len("</private") : gt]) {
				if !asciiSpace(c) {
					ws = false
					break
				}
			}
			if ws {
				closers = append(closers, privateTag{i, gt + 1})
			}
		}
	}
	return openers, closers
}

// stripPrivate removes every <private> element from s, content included. It
// tracks nesting depth so an inner <private> never ends the outer element
// early (a non-greedy regex would leave the outer tail indexed — caught by the
// final whole-change verifier). Two sibling blocks remove exactly two spans;
// an unterminated opener leaves the text from that opener on intact. It runs
// before secret redaction so a secret inside a private block vanishes without
// leaving a [REDACTED:…] marker that would reveal one was there. The scan is
// block-local by construction: it only ever sees one string.
//
// Linear time. The tags are scanned once; the nesting is then resolved with one
// stack pass over the tag stream plus a right-to-left table, instead of
// re-searching for a closer at every opener (cubic on thousands of nested
// openers). The result is byte-identical to that earlier search, which stays in
// the tests as stripPrivateReference. Two details are inherited from it:
//   - inside an element the next tag is "the first opener, if it ends before the
//     next closer starts, else that closer", so an opener whose body swallows a
//     closer (`<private </private>`) counts as the closer there; at top level the
//     leftmost opener wins even then. Nesting is therefore resolved on the
//     inside-view stream (body) while top-level picks use all openers.
//   - an opener with no matching closer is kept as literal text and the scan
//     resumes right after it, so a later complete element is still removed.
func stripPrivate(s string) string {
	if strings.IndexByte(s, '<') < 0 {
		return s // no tag at all: skip the scan on every plain message
	}
	openers, closers := scanPrivateTags(s)
	if len(openers) == 0 {
		return s
	}

	// body: the tag stream as seen from inside an element. At position i the next
	// tag is the first opener at/after i if it ends before the first closer
	// at/after i starts, else that closer; with no closer left the stream ends
	// (an open element can never be closed).
	type bodyTag struct {
		start, end int
		open       bool
	}
	var body []bodyTag
	{
		oi, ci, i := 0, 0, 0
		for {
			for ci < len(closers) && closers[ci].start < i {
				ci++
			}
			if ci == len(closers) {
				break
			}
			for oi < len(openers) && openers[oi].start < i {
				oi++
			}
			if oi < len(openers) && openers[oi].end <= closers[ci].start {
				body = append(body, bodyTag{openers[oi].start, openers[oi].end, true})
				i = openers[oi].end
			} else {
				body = append(body, bodyTag{closers[ci].start, closers[ci].end, false})
				i = closers[ci].end
			}
		}
	}

	// match[k] for an opener body[k]: index of the closer that balances it, -1 if none.
	match := make([]int, len(body))
	var stack []int
	for k, t := range body {
		match[k] = -1
		if t.open {
			stack = append(stack, k)
		} else if n := len(stack); n > 0 {
			match[stack[n-1]] = k
			stack = stack[:n-1]
		}
	}
	// strayFrom[k]: the first closer in body[k:] with no opener to pair with inside
	// body[k:], i.e. the closer that ends an element whose opener ends just before
	// body[k]. -1 if there is none (the element is unterminated).
	strayFrom := make([]int, len(body)+1)
	strayFrom[len(body)] = -1
	for k := len(body) - 1; k >= 0; k-- {
		switch {
		case !body[k].open:
			strayFrom[k] = k
		case match[k] >= 0:
			strayFrom[k] = strayFrom[match[k]+1]
		default:
			strayFrom[k] = -1
		}
	}

	var out strings.Builder
	pos, oi, bk := 0, 0, 0
	for {
		for oi < len(openers) && openers[oi].start < pos {
			oi++
		}
		if oi == len(openers) {
			break
		}
		o := openers[oi]
		for bk < len(body) && body[bk].start < o.end {
			bk++
		}
		if end := strayFrom[bk]; end >= 0 {
			out.WriteString(s[pos:o.start])
			pos = body[end].end
		} else {
			// Unterminated: this opener is literal text. Keep it and resume
			// the scan right after it, so a later complete element is still
			// removed (a stray "<private>" earlier in a message must not
			// shield the real one that follows — found on the live index).
			out.WriteString(s[pos:o.end])
			pos = o.end
		}
	}
	out.WriteString(s[pos:])
	return out.String()
}

func init() {
	if err := validateRedactRules(redactRules); err != nil {
		panic(err)
	}
}

// foldLower lower-cases s the way Go's (?i) matching sees ASCII letters:
// strings.ToLower (which maps U+212A to k) plus U+017F to s. The result is only
// used for substring tests of lower-case ASCII needles; extra spurious letters
// it may produce merely let a regexp run.
func foldLower(s string) string {
	l := strings.ToLower(s) // no allocation when s has no upper-case letter
	if strings.Contains(l, "\u017f") {
		l = strings.ReplaceAll(l, "\u017f", "s")
	}
	return l
}

// mayMatch reports whether r could match s. It is a necessary condition only:
// false means the regexp provably cannot match (skip it), true means "run it".
// lower returns foldLower(s) on demand, so the copy is built only for a
// foldCase rule that got past the cheap checks.
func (r *redactRule) mayMatch(s string, lower func() string) bool {
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

// Redact replaces recognized secret patterns in s. It is intentionally
// conservative; see redactRules. Each rule's regexp only runs when its cheap
// necessary-condition prefilter passes; the output is identical to running
// every rule (redactUnfiltered, pinned by tests and a real-corpus comparison).
func Redact(s string) string { return redactWith(s, true, nil) }

// redactWith is Redact with the prefilter switchable (filtered=false runs every
// rule, the reference path). If ran is non-nil it receives the index of every
// rule whose regexp was executed, so tests can observe what was skipped.
func redactWith(s string, filtered bool, ran *[]int) string {
	return applyRules(redactRules, s, filtered, ran)
}

// applyRules is the rule loop behind redactWith, over an explicit rule slice so
// tests can drive it with their own tables.
func applyRules(rules []redactRule, s string, filtered bool, ran *[]int) string {
	var lower string
	haveLower := false
	getLower := func() string {
		if !haveLower {
			lower, haveLower = foldLower(s), true
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
		if ns := r.replace(s); ns != s {
			s = ns
			haveLower = false // text changed: rebuild the folded copy on demand
		}
	}
	return s
}

// redactUnfiltered runs every rule on s with no prefilter. It is the reference
// implementation Redact must match byte for byte.
func redactUnfiltered(s string) string {
	for _, r := range redactRules {
		s = r.replace(s)
	}
	return s
}

// secretKeyRe matches secret-named keys with word boundaries to avoid false
// positives (e.g. "tokenizer", "author", "oauth_provider").
// Compound terms use spaces because isSecretKey normalizes underscores to spaces.
var secretKeyRe = regexp.MustCompile(`\b(password|passwd|passphrase|secret|secrets|credential|credentials|token|apikey|api key|accesskey|access key|privatekey|private key|secret key|auth token|dsn|connection string|conn str)\b`)

// secretKeyExact are secret-bearing key names matched only as the WHOLE normalized
// key (after underscore/hyphen/camelCase splitting), not as a substring — so
// "authorization"/"cookie"/"set cookie" are caught but "authorizationURL",
// "cookieJar", and "setCookieBanner" are not.
var secretKeyExact = map[string]bool{
	"authorization": true, "proxy authorization": true,
	"cookie": true, "set cookie": true,
}

// Match normalized credential suffixes in structured JSON too. A bare pwd is
// the working directory; pass alone is ambiguous outside a credential context.
var credentialSuffixRe = regexp.MustCompile(`^(?:(?:db|mysql|smtp|redis|admin|root|user|mail|ftp|ssh|ldap|pg|postgres) pass|.+ pwd)$`)

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
	if secretKeyExact[n] || credentialSuffixRe.MatchString(n) {
		return true
	}
	return secretKeyRe.MatchString(n)
}

type jsonSpan struct {
	end   int // exclusive end; zero means unbalanced
	valid bool
}

// balancedJSONSpans records bracket spans in O(n) time and space. A mismatched
// closer terminates all open candidates in that string context. Quotes with an
// odd preceding backslash run do not toggle the context.
//
// There are two stacks, one for each quote parity. The second stack matters:
// main probes EVERY opener, even inside quoted prose or an invalid JSON string.
// A candidate starts outside its own strings regardless of the prose's parity.
// Thus brackets in a candidate's strings cannot close it, but can still begin
// independent candidates, preserving main's coverage without boundary guesses.
//
// Validate each balanced span's shallow syntax once: replace balanced children
// with a spaced scalar (or an invalid sentinel). Each original byte belongs to
// at most one shallow buffer per parity. This also makes balanced INVALID nests
// linear: no decoder repeatedly reads their descendants. Only valid candidates
// reach the decoder, outermost first; success skips all enclosed positions.
func balancedJSONSpans(s string) []jsonSpan {
	spans := make([]jsonSpan, len(s))
	type frame struct {
		start, depth int
		shallow      []byte
	}
	var stacks [2][]frame
	parity, slashes := 0, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		// This byte is ordinary string content in the other quote context.
		other := &stacks[1-parity]
		if n := len(*other); n > 0 {
			(*other)[n-1].shallow = append((*other)[n-1].shallow, c)
		}
		stack := &stacks[parity]
		switch c {
		case '{', '[':
			*stack = append(*stack, frame{start: i, depth: 1, shallow: []byte{c}})
		case '}', ']':
			n := len(*stack)
			if n > 0 {
				f := (*stack)[n-1]
				if (s[f.start] == '{' && c != '}') || (s[f.start] == '[' && c != ']') {
					*stack = (*stack)[:0]
				} else {
					f.shallow = append(f.shallow, c)
					valid := json.Valid(f.shallow) && f.depth <= 10000 // encoding/json's nesting limit
					spans[f.start] = jsonSpan{i + 1, valid}
					*stack = (*stack)[:n-1]
					if n > 1 {
						parent := &(*stack)[n-2]
						if f.depth+1 > parent.depth {
							parent.depth = f.depth + 1
						}
						if valid {
							parent.shallow = append(parent.shallow, ' ', '0', ' ')
						} else {
							parent.shallow = append(parent.shallow, ' ', '!', ' ')
						}
					}
				}
			}
		default:
			if n := len(*stack); n > 0 {
				(*stack)[n-1].shallow = append((*stack)[n-1].shallow, c)
			}
		}
		if c == '"' && slashes%2 == 0 {
			parity = 1 - parity
		}
		if c == '\\' {
			slashes++
		} else {
			slashes = 0
		}
	}
	return spans
}

// redactString is the single text redactor used for message content, session
// titles, and string leaves inside JSON.
//
// It structurally redacts any JSON object/array embedded ANYWHERE in the string
// (so a secret under a generic key like {"apiKey":"…"} is caught even amid
// surrounding prose), and applies the shape regexes to the non-JSON text around
// those spans. Free-text secrets with no recognizable shape are best-effort.
func redactString(s string) string { return redactStringWith(s, true) }

// redactStringWith is redactString with the regex prefilter switchable, so tests
// can compare the filtered and unfiltered paths through the whole JSON walk.
func redactStringWith(s string, filtered bool) string {
	// Strip <private> first, while the string is still whole: the JSON split
	// below would otherwise hand a private block containing JSON to the regex
	// in pieces that never match.
	s = stripPrivate(s)
	if !strings.ContainsAny(s, "{[") {
		return redactWith(s, filtered, nil) // fast path: no JSON to find
	}
	var out strings.Builder
	textStart := 0
	spans := balancedJSONSpans(s)
	for i := 0; i < len(s); {
		if span := spans[i]; span.valid {
			if red, n, ok := redactJSONAt(s[i:span.end], filtered); ok {
				out.WriteString(redactWith(s[textStart:i], filtered, nil)) // regex-redact preceding prose
				out.WriteString(red)                                       // structurally-redacted JSON span
				i += n
				textStart = i
				continue
			}
		}
		i++
	}
	out.WriteString(redactWith(s[textStart:], filtered, nil))
	return out.String()
}

// redactMemo remembers the last redactString result. One tool call redacts the
// same input string for its summary and again for its activity target; sharing
// the memo makes that one pass with a byte-identical result (redactString is a
// pure function of its input). A nil *redactMemo just calls redactString.
// debt: the third pass over the same string (the raw_json walk in redactJSON)
// and the final pass over name+" "+summary are not shared: redaction is not
// idempotent in general (TestToolUseContentKeepsSecondRedactionPass), so only
// passes over the identical input can be merged without changing output.
//
// debt: 工具輸入 JSON 仍被 summary 與 target 各解析一次；在呼叫點解析一次並把選定值傳給兩邊即可移除 memo
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
func redactJSONAt(t string, filtered bool) (string, int, bool) {
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
	if err := enc.Encode(redactWalkDepth(v, 0, filtered)); err != nil {
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
func redactWalk(v any) any { return redactWalkDepth(v, 0, true) }

func redactWalkDepth(v any, depth int, filtered bool) any {
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
				val = redactWalkDepth(x[k], depth+1, filtered)
			}
			nk := redactStringWith(k, filtered)
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
			x[i] = redactWalkDepth(x[i], depth+1, filtered)
		}
		return x
	case string:
		return redactStringWith(x, filtered) // recurse into JSON-in-string + regex
	default:
		return v // numbers (json.Number), bools, null
	}
}
