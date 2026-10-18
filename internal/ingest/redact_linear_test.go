package ingest

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"time"
)

// privateOpenRe / privateCloseRe are the regexps the hand-written tag scanner
// replaced; they now live only here, as part of the oracle below.
var (
	privateOpenRe  = regexp.MustCompile(`(?i)<private(?:\s[^>]*[^/>])?\s*>`)
	privateCloseRe = regexp.MustCompile(`(?i)</private\s*>`)
)

// stripPrivateReference is the pre-linearisation implementation, kept verbatim
// as the oracle for the single-pass version: same outputs on every input is the
// contract, and it is cubic on nested openers, so only small inputs may use it.
func stripPrivateReference(s string) string {
	if strings.IndexByte(s, '<') < 0 {
		return s
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
				break
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

var privateFragments = []string{
	"<private>", "</private>", "<PRIVATE>", "</Private >", "<private/>", "<private />", "<private a>", `<private reason="x">`,
	"<private a/>", "<private a/ >", "<private  >", "<private\t>", "<private\n>", "<privately>", "<privatex>", "<private",
	"</private", "</private  >", "</private x>", "<private </private>", "<private <private>", "<private\v>", "<private a\v>",
	"</privat>", "<", ">", "/", " ", "\n", "x", "secret", "é", "<p>",
}

func randomPrivateInput(r *rand.Rand, maxFrags int) string {
	var b strings.Builder
	for n := r.Intn(maxFrags); n >= 0; n-- {
		b.WriteString(privateFragments[r.Intn(len(privateFragments))])
	}
	return b.String()
}

// The single-pass stripPrivate must equal the reference on hand-picked shapes
// (nesting, stray tags, attributes, case, an opener whose body swallows a
// closer) and on a large random sample.
func TestStripPrivateMatchesReference(t *testing.T) {
	for _, in := range []string{
		"", "plain", "<", "<private>", "</private>", "<private>x</private>", "a <private>x b", "a x</private> b",
		"x <private>alpha <private>beta</private> gamma</private> y",
		"<private><private>s</private></private> y <private>t</private> z",
		"<private> a <private> b <private>s</private> c",
		"x </private> y <private> z <private>s</private> w",
		"<private></private><private></private>", "<private><private></private>", "<private></private></private>",
		"<private </private> tail", "<private <private> </private>", "x <private </private>y</private> z",
		"<private a/>x</private>", "<private a/ >x</private>", "<private\v>x</private>",
		"<PRIVATE>a</Private>", "<private>é</private>é", "<private>" + strings.Repeat("<private>", 50) + strings.Repeat("</private>", 49),
		strings.Repeat("<private>", 30) + "x" + strings.Repeat("</private>", 30),
		strings.Repeat("<private>", 30) + "x</private>",
	} {
		if got, want := stripPrivate(in), stripPrivateReference(in); got != want {
			t.Errorf("stripPrivate(%q)\n got  %q\n want %q", in, got, want)
		}
	}
	r := rand.New(rand.NewSource(20261018))
	for i := 0; i < 300000; i++ {
		in := randomPrivateInput(r, 14)
		if got, want := stripPrivate(in), stripPrivateReference(in); got != want {
			t.Fatalf("stripPrivate(%q)\n got  %q\n want %q", in, got, want)
		}
	}
}

// The scenario in the spec: thousands of nested openers and one closer leave
// every opener but the last as literal text and drop the last element.
func TestStripPrivateNestedOpenersOneCloser(t *testing.T) {
	in := "head " + strings.Repeat("<private>", 2000) + "secret</private> tail"
	want := "head " + strings.Repeat("<private>", 1999) + " tail"
	if got := stripPrivate(in); got != want {
		t.Errorf("stripPrivate: got %d bytes, want %d bytes (mismatch)", len(got), len(want))
	}
}

func FuzzStripPrivateMatchesReference(f *testing.F) {
	for _, s := range privateFragments {
		f.Add(s + "x" + s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := stripPrivate(s), stripPrivateReference(s); got != want {
			t.Fatalf("stripPrivate(%q)\n got  %q\n want %q", s, got, want)
		}
	})
}

// redactStringReference is redactString as it was before the probe limits:
// the pre-linearisation stripPrivate and a decoder probe at every '{' / '['.
func redactStringReference(s string) string {
	s = stripPrivateReference(s)
	if !strings.ContainsAny(s, "{[") {
		return redactWith(s, true, nil)
	}
	var out strings.Builder
	textStart := 0
	for i := 0; i < len(s); {
		if s[i] == '{' || s[i] == '[' {
			if red, n, ok := redactJSONAt(s[i:], true); ok {
				out.WriteString(redactWith(s[textStart:i], true, nil))
				out.WriteString(red)
				i += n
				textStart = i
				continue
			}
		}
		i++
	}
	out.WriteString(redactWith(s[textStart:], true, nil))
	return out.String()
}

var jsonProbeFragments = []string{
	` {"apiKey":"abcdef123456"}`, ` {"a":"ok","token":"zzzzzzzz"}`, ` [1,2,3]`, ` ["x",{"password":"hunter2hunter2"}]`,
	` {a, b}`, ` [x`, ` {`, ` ["a"`, ` {"a":`, ` [[1,2],[3]]`, `:{"k":"v"}`, `,[1]`, `=[2]`, `({"u":1})`,
	"\n{\"secret\":\"abcdefgh\"}", "\t[]", " {}", `a[i]`, `x{`, ` {"nested":{"token":"abcdefgh"}} trailing`,
}

// Bracket handling: the probe limits (boundary char, failure cap) only skip
// brackets that cannot start embedded JSON a person would write, so for every
// input whose brackets sit at a boundary and fail fewer than the cap, the output
// equals the unrestricted probe-everything version.
func TestRedactStringMatchesReferenceAtBoundaries(t *testing.T) {
	r := rand.New(rand.NewSource(20261018))
	for i := 0; i < 60000; i++ {
		var b strings.Builder
		for n := r.Intn(10); n >= 0; n-- {
			switch r.Intn(3) {
			case 0:
				b.WriteString(jsonProbeFragments[r.Intn(len(jsonProbeFragments))])
			case 1:
				b.WriteString(randomPrivateInput(r, 4))
			default:
				b.WriteString(randomRedactInput(r))
			}
		}
		in := b.String()
		// keep the comparison inside the domain: every bracket at a boundary
		ok := true
		for j := 0; j < len(in); j++ {
			if (in[j] == '{' || in[j] == '[') && !jsonProbeBoundary(in, j) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if got, want := redactString(in), redactStringReference(in); got != want {
			t.Fatalf("redactString(%q)\n got  %q\n want %q", in, got, want)
		}
	}
}

// jsonProbeBoundary admits exactly: text start, whitespace, : , = ( " ' ` [ { ] }.
func TestJSONProbeBoundary(t *testing.T) {
	for _, c := range []string{" ", "\t", "\n", "\r", ":", ",", "=", "(", `"`, "'", "`", "[", "{", "]", "}"} {
		if !jsonProbeBoundary(c+"{", 1) {
			t.Errorf("%q before a bracket should be a boundary", c)
		}
	}
	if !jsonProbeBoundary("{", 0) {
		t.Error("text start should be a boundary")
	}
	for _, c := range []string{"a", "9", "_", ".", ")", ";", "<", "!", "\u00e9", "\uff1a"} {
		if jsonProbeBoundary(c+"{", len(c)) {
			t.Errorf("%q before a bracket should not be a boundary", c)
		}
	}
}

// Embedded JSON after a realistic number of failed probes (code, prose with
// stray brackets) must still be structurally redacted; the cap exists only for
// floods. (What happens past the cap is the debt on maxFailedJSONProbes, not a
// behaviour to pin.)
func TestRedactStringJSONAfterSomeFailedProbes(t *testing.T) {
	const secretJSON = `{"apiKey":"abcdef123456"}`
	in := strings.Repeat("[ ", maxFailedJSONProbes/2) + secretJSON
	if got := redactString(in); strings.Contains(got, "abcdef123456") {
		t.Errorf("JSON after %d failed probes must still be redacted: %q", maxFailedJSONProbes/2, got)
	}
}

// adversarialLine builds the 18 KB single-line shapes that made the old code
// cubic / quadratic.
func adversarialPrivate() string { return strings.Repeat("<private>", 2000) + "</private>" } // 18,010 bytes: 2000 nested openers, one closer

func adversarialBrackets() string { return strings.Repeat("[", 18000) }

func adversarialNestedObjects() string { return strings.Repeat(`{"a":`, 3600) } // 18,000 bytes

// linearBudget is the ceiling for the adversarial inputs. The single-pass code
// needs well under 10 ms here, the old code needed 18 s (stripPrivate) and
// seconds (the bracket floods); 500 ms leaves two orders of magnitude of
// headroom for a slow or loaded CI machine while still failing any
// super-linear regression.
const linearBudget = 500 * time.Millisecond

func TestAdversarialInputsStayLinear(t *testing.T) {
	cases := []struct {
		name string
		in   string
		fn   func(string) string
	}{
		{"stripPrivate: 2000 nested openers, one closer, one line", adversarialPrivate(), stripPrivate},
		{"stripPrivate: openers only", strings.Repeat("<private>", 2000), stripPrivate},
		{"stripPrivate: openers, one fewer closers", strings.Repeat("<private>", 1000) + strings.Repeat("</private>", 999), stripPrivate},
		{"stripPrivate: balanced nesting", strings.Repeat("<private>", 1000) + strings.Repeat("</private>", 1000), stripPrivate},
		{"stripPrivate: alternating", strings.Repeat("<private></private></private>", 600), stripPrivate},
		{"redactString: 18 KB of [", adversarialBrackets(), redactString},
		{"redactString: 18 KB of {\"a\":", adversarialNestedObjects(), redactString},
		{"redactString: spaced brackets", strings.Repeat("[ ", 9000), redactString},
		{"redactString: private flood", adversarialPrivate(), redactString},
	}
	for _, c := range cases {
		start := time.Now()
		c.fn(c.in)
		if d := time.Since(start); d > linearBudget {
			t.Errorf("%s: took %v, want < %v", c.name, d, linearBudget)
		}
	}
}

func BenchmarkStripPrivateNested2000(b *testing.B) {
	in := adversarialPrivate()
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		stripPrivate(in)
	}
}

func BenchmarkRedactStringBracketFlood(b *testing.B) {
	in := adversarialBrackets()
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		redactString(in)
	}
}

func BenchmarkRedactStringNestedObjects(b *testing.B) {
	in := adversarialNestedObjects()
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		redactString(in)
	}
}
