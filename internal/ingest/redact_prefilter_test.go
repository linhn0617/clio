package ingest

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// prefilterCases returns hand-written positive and negative inputs for every
// rule, the Unicode case-folding traps, and \b edges.
func prefilterCases() []string {
	return []string{
		// one positive per rule
		"-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY-----",
		"AKIAIOSFODNN7EXAMPLE", "AIza01234567890123456789012345678901234",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789", "gho_abcdefghijklmnopqrstuvwxyz0123456789",
		"ghu_abcdefghijklmnopqrstuvwxyz0123456789", "ghs_abcdefghijklmnopqrstuvwxyz0123456789",
		"ghr_abcdefghijklmnopqrstuvwxyz0123456789",
		"github_pat_abcdefghijklmnopqrstuvwxyz0123456789",
		"xoxb-1234567890-abcdef", "xoxa-1234567890-abcdef", "xoxp-1234567890-abcdef",
		"sk-abcdefghijklmnopqrstuvwxyz0123", "sk_live_abcdefghij0123", "rk_test_abcdefghij0123", "pk_live_abcdefghij0123",
		"eyJhbGciOiJI.eyJzdWIiOiIx.abcdefghij12",
		"Bearer abcdef123456ghijkl", "BEARER abcdef123456ghijkl", "bearer abcdef123456ghijkl",
		"Authorization: Basic dXNlcjpwYXNz", "proxy-authorization: basic QUJD", "AUTHORIZATION:BASIC QUJD",
		"Cookie: a=b; c=d", "set-cookie: sid=abc; Path=/", "COOKIE: x=y",
		"API_KEY=supersecretvalue123", "db_password: hunter2hunter2", "MY_TOKEN = \"abcdefgh\"",
		"secret=abcdefgh", "ACCESS-KEY: abcdefgh", "private_key=abcdefgh", "credential: abcdefgh", "passwd=abcdefgh",
		"postgres://user:pass@host/db", "HTTP://u:p@h", "redis://h:p@x",
		// negatives that carry a needle but not the full shape
		"AKIA short", "sk-short", "xox no", "eyJ.only", "Bearer short", "cookie: enabled",
		"key and token words with no assignment", "https://example.com", "https://user@host",
		"plain prose with nothing in it", "", "a", "[]", "{}",
		// Unicode simple-folding traps for the (?i) rules: U+212A KELVIN SIGN folds
		// to k, U+017F LONG S folds to s.
		"API_\u212aEY=supersecretvalue123", "PASSWORD=abcdefgh", "TOKEN=abcdefgh",
		"api_\u212aey=supersecretvalue123", "\u212aEY=abcdefgh", "SECRET=abcdefgh",
		"\u017fecret=abcdefgh", "pa\u017f\u017fword=abcdefgh", "PA\u017f\u017fWORD: abcdefgh",
		"to\u212aen=abcdefgh", "TO\u212aEN: abcdefgh", "cooMie: a=b", "coo\u212aie: a=b", "Coo\u212aie: a=b",
		"Bearer\u00a0abcdef123456ghijkl", "bearer abcdef123456ghijkl\u212a",
		"\u017fet-cookie: a=b", "Author\u0131zation: Basic QUJD", "authorization\uff1a Basic QUJD",
		"\uff21\uff2b\uff29\uff21abc", "\uff53\uff4b-abcdefghijklmnopqrstuvwxyz0123",
		"AKIA\u212aIOSFODNN7EXAMPLE", "sk-abcdefghijklmnopqrstuvwxyz0123\u00e9", "\u00e9sk-abcdefghijklmnopqrstuvwxyz0123",
		"\u00e9AKIAIOSFODNN7EXAMPLE", "AKIAIOSFODNN7EXAMPLE\u00e9", "scheme\u212a://u:p@h", "\u4e2d\u6587 API_KEY=supersecretvalue123 \u4e2d\u6587",
		// \b edges
		"xAKIAIOSFODNN7EXAMPLE", "AKIAIOSFODNN7EXAMPLEx", "_sk-abcdefghijklmnopqrstuvwxyz0123", "sk-abcdefghijklmnopqrstuvwxyz0123_",
		"Bearer abcdef123456ghijkl.", "(ghp_abcdefghijklmnopqrstuvwxyz0123456789)",
		// markers produced by earlier redaction
		"API_KEY=[REDACTED:secret]", "Bearer [REDACTED:token]", "Cookie: [REDACTED:cookie]", "X=[REDA", "TOKEN=[REDACTED:sec",
		"[REDACTED:private-key]", "<private>sk-abcdefghijklmnopqrstuvwxyz0123</private> tail",
	}
}

// literalsInRedactTests collects every string literal in redact_test.go so the
// equivalence check covers all existing cases without copying them.
func literalsInRedactTests(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "redact_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				out = append(out, s)
			}
		}
		return true
	})
	if len(out) < 50 {
		t.Fatalf("expected many literals in redact_test.go, got %d", len(out))
	}
	return out
}

func checkSame(t *testing.T, in string) {
	t.Helper()
	if got, want := Redact(in), redactUnfiltered(in); got != want {
		t.Fatalf("prefilter changed output\nin:   %q\ngot:  %q\nwant: %q", in, got, want)
	}
}

func TestPrefilterEquivalentOnKnownCases(t *testing.T) {
	for _, s := range prefilterCases() {
		checkSame(t, s)
	}
	for _, s := range literalsInRedactTests(t) {
		checkSame(t, s)
	}
}

// The rule table's safety constraints are enforced at init (panic); this checks
// the validator itself rejects the two ways a table can silently go unsafe.
func TestValidateRedactRules(t *testing.T) {
	if err := validateRedactRules(redactRules); err != nil {
		t.Fatalf("shipped table invalid: %v", err)
	}
	ci := regexp.MustCompile(`(?i)abc`)
	cs := regexp.MustCompile(`abc`)
	bad := map[string]redactRule{
		"foldCase but pattern case-sensitive": {re: cs, needles: []string{"abc"}, foldCase: true},
		"(?i) pattern but foldCase unset":     {re: ci, needles: []string{"abc"}},
		"upper-case needle on foldCase rule":  {re: ci, needles: []string{"ABC"}, foldCase: true},
	}
	for name, r := range bad {
		if err := validateRedactRules([]redactRule{r}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if err := validateRedactRules([]redactRule{{re: cs}}); err != nil {
		t.Errorf("nil needles (always run) must be valid: %v", err)
	}
}

// Go's (?i) folds U+017F LONG S to s and U+212A KELVIN SIGN to k; foldLower
// must keep the (?i) rules reachable for both without any non-ASCII fallback.
func TestPrefilterNonASCIIFoldsStillRedact(t *testing.T) {
	for _, in := range []string{
		"API_\u212aEY=supersecretvalue123",
		"pa\u017f\u017fword=abcdefgh",
		"coo\u212aie: a=b",
		"\u017fet-cookie: a=b",
		"\u4e2d\u6587 x\u017fecret=abcdefgh",
	} {
		var ran []int
		got := redactWith(in, true, &ran)
		if !strings.Contains(got, "[REDACTED") {
			t.Errorf("expected redaction of %q, got %q (ran rules %v)", in, got, ran)
		}
		checkSame(t, in)
	}
}

// Text with non-ASCII letters but no secret feature must still be filtered:
// the KEY=value (12) and Cookie (11) rules are skipped, and the output equals
// the unfiltered run.
func TestPrefilterSkipsRulesOnCJKText(t *testing.T) {
	for _, in := range []string{
		"\u9a57\u8b49\u6d41\u7a0b\u5c0d\u4f7f\u7528\u8005\u7684\u8aaa\u660e",
		"\u4e2d\u6587 plain text with a colon: and equals = signs",
	} {
		var ran []int
		got := redactWith(in, true, &ran)
		for _, idx := range ran {
			if idx == 11 || idx == 12 {
				t.Errorf("rule %d ran on %q; ran=%v", idx, in, ran)
			}
		}
		if got != redactUnfiltered(in) {
			t.Errorf("output differs for %q: %q", in, got)
		}
	}
	// A secret-bearing CJK text still runs the rule that matters.
	var ran []int
	in := "\u4e2d\u6587 API_KEY=supersecretvalue123"
	got := redactWith(in, true, &ran)
	if !strings.Contains(got, "[REDACTED:secret]") {
		t.Fatalf("got %q", got)
	}
}

// On every realistic input in the suite (the known cases and all literals of
// redact_test.go) redaction is idempotent. It is NOT idempotent in general: see
// TestRedactNotIdempotentOnAdversarialText.
func TestRedactIdempotentOnKnownCases(t *testing.T) {
	inputs := append(prefilterCases(), literalsInRedactTests(t)...)
	for _, s := range inputs {
		once := Redact(s)
		if twice := Redact(once); twice != once {
			t.Errorf("Redact not idempotent\nin:    %q\nonce:  %q\ntwice: %q", s, once, twice)
		}
		if strings.ContainsAny(s, "{[") {
			continue
		}
		onceS := redactString(s)
		if twiceS := redactString(onceS); twiceS != onceS {
			t.Errorf("redactString not idempotent\nin:    %q\nonce:  %q\ntwice: %q", s, onceS, twiceS)
		}
	}
}

// A tool call's content is redactString(name + " " + summary) where summary is
// already firstLine(redactString(cmd), 200): that second pass must stay, because
// redaction is not idempotent on these inputs (the first pass leaves text the
// second one still changes). The test pins that the stored content equals the
// two-pass result, and that the inputs really are ones where one pass differs.
func TestToolUseContentKeepsSecondRedactionPass(t *testing.T) {
	cmds := []string{
		`{"ghp_abcdefghijklmnopqrstuvwxyz0123456789":1}`,
		"passwordcookie:<private>@pk_test_[REDACTED:secret]'-----BEGIN API_KEY" + "API_KEYhttp9",
	}
	for _, cmd := range cmds {
		cmdJSON, _ := json.Marshal(cmd)
		line := `{"type":"assistant","timestamp":"2026-04-26T11:00:00Z","cwd":"/p","sessionId":"s","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":` + string(cmdJSON) + `}}]}}`
		msgs, _, err := NewParser(0).ParseLine([]byte(line))
		if err != nil || len(msgs) != 1 {
			t.Fatalf("parse: %v %d", err, len(msgs))
		}
		summary := firstLine(redactString(cmd), 200)
		onePass := "Bash " + summary
		twoPass := strings.TrimSpace(redactString("Bash " + summary))
		if onePass == twoPass {
			t.Fatalf("test input %q no longer distinguishes one pass from two", cmd)
		}
		if msgs[0].Content != twoPass {
			t.Errorf("content lost the second redaction pass\n got:  %q\n want: %q", msgs[0].Content, twoPass)
		}
	}
}

var fuzzFragments = []string{
	"AKIA", "AIza", "ghp_", "gho_", "github_pat_", "xoxb-", "xoxs-", "sk-", "sk_live_", "pk_test_", "eyJ", ".", "-", "_",
	"Bearer ", "bearer\t", "BEARER ", "Authorization: ", "Proxy-Authorization:", " Basic ", "Cookie: ", "Set-Cookie:", "cookie:",
	"API_KEY", "api-key", "ACCESS_KEY", "private_key", "SECRET", "password", "PASSWD", "token", "Credential", "key",
	"=", ":", " ", "\n", "\t", "\"", "'", "://", "@", "user:pass", "http", "postgres",
	"-----BEGIN RSA PRIVATE KEY-----", "-----END RSA PRIVATE KEY-----", "PRIVATE KEY", "-----BEGIN ", "-----END ",
	"abcdefghijklmnopqrstuvwxyz", "ABCDEFGHIJKLMNOPQRST", "0123456789", "xyz", "a", "Z", "9",
	"\u212a", "\u017f", "\u0131", "\u00e9", "\uff21", "\u4e2d", "\u00a0", "[REDACTED:secret]", "[REDACTED:token]", "{", "}", "[", "]", "<private>", "</private>",
}

func randomRedactInput(r *rand.Rand) string {
	var b strings.Builder
	for n := r.Intn(14); n >= 0; n-- {
		b.WriteString(fuzzFragments[r.Intn(len(fuzzFragments))])
	}
	return b.String()
}

func TestPrefilterEquivalentRandom(t *testing.T) {
	r := rand.New(rand.NewSource(20261017))
	for i := 0; i < 60000; i++ {
		s := randomRedactInput(r)
		checkSame(t, s)
	}
}

func FuzzPrefilterEquivalent(f *testing.F) {
	for _, s := range prefilterCases() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		checkSame(t, s)
	})
}

// redactString must give the same result with the prefilter on and off, since
// it routes embedded JSON through the structural walk plus Redact.
func TestRedactStringEquivalentWithAndWithoutPrefilter(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	inputs := append(prefilterCases(), literalsInRedactTests(t)...)
	for i := 0; i < 20000; i++ {
		inputs = append(inputs, randomRedactInput(r)+`{"apiKey":"x`+randomRedactInput(r)+`"}`+randomRedactInput(r))
	}
	for _, s := range inputs {
		on := redactStringWith(s, true)
		off := redactStringWith(s, false)
		if on != off {
			t.Fatalf("redactString differs\nin:  %q\non:  %q\noff: %q", s, on, off)
		}
	}
}

// Go's (?i) folds exactly two non-ASCII runes onto ASCII letters (U+212A to k,
// U+017F to s). foldLower must map each back to that letter, so ASCII needles
// find them without a non-ASCII fallback. Pinned against the running Go version.
func TestSimpleFoldOnlyKAndS(t *testing.T) {
	got := map[rune]rune{}
	for r := rune(0x80); r <= unicode.MaxRune; r++ {
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < 0x80 && unicode.IsLetter(f) {
				got[r] = unicode.ToLower(f)
			}
		}
	}
	if len(got) != 2 || got[0x212a] != 'k' || got[0x017f] != 's' {
		t.Fatalf("non-ASCII runes folding onto ASCII letters = %U, want {U+212A:k U+017F:s}", got)
	}
	for r, letter := range got {
		if fl := foldLower(string(r)); fl != string(letter) {
			t.Errorf("foldLower(%U) = %q, want %q", r, fl, string(letter))
		}
	}
}

// The memo must return exactly what redactString returns, and reuse it for an
// identical input rather than redacting again.
func TestRedactMemo(t *testing.T) {
	in := "export API_KEY=supersecretvalue123 && go test"
	var rm redactMemo
	first := rm.redact(in)
	if first != redactString(in) {
		t.Fatalf("memo result %q differs from redactString %q", first, redactString(in))
	}
	rm.out = "SENTINEL" // a second call with the same input must come from the memo
	if got := rm.redact(in); got != "SENTINEL" {
		t.Fatalf("identical input was redacted again: got %q", got)
	}
	if got := rm.redact("other API_KEY=anothersecret1234"); got == "SENTINEL" || !strings.Contains(got, "[REDACTED") {
		t.Fatalf("different input must be redacted afresh, got %q", got)
	}
	var nilMemo *redactMemo
	if got := nilMemo.redact(in); got != redactString(in) {
		t.Fatalf("nil memo must fall back to redactString, got %q", got)
	}
}

// Sharing the memo between summary and target must not change either result.
func TestToolUseMemoSameAsUnmemoized(t *testing.T) {
	inputs := []string{
		`{"command":"curl -H 'Authorization: Bearer abcdef123456ghijkl' https://x"}`,
		`{"command":"{\"ghp_abcdefghijklmnopqrstuvwxyz0123456789\":1} tail"}`,
		`{"file_path":"/tmp/API_KEY=abcdefghij/x.go"}`,
		`{"command":"echo KEY=abcdefgh"}`,
	}
	for _, in := range inputs {
		for _, tool := range []string{"Bash", "Read"} {
			var rm redactMemo
			s1, t1 := toolUseSummaryMemo([]byte(in), &rm), extractTargetsMemo(tool, []byte(in), &rm)
			s2, t2 := toolUseSummary([]byte(in)), extractTargets(tool, []byte(in))
			if s1 != s2 || !reflect.DeepEqual(t1, t2) {
				t.Fatalf("memoized differs for %s %s\n%q %v\n%q %v", tool, in, s1, t1, s2, t2)
			}
		}
	}
}
