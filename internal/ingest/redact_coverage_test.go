package ingest

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"
)

func TestRedactionJSONCoverage(t *testing.T) {
	for _, prefix := range []string{"result->", "<pre>", "resp|", strings.Repeat("[x ", 128), strings.Repeat("[INFO] request ok\n", 130), "x", ">", "=>", "->", "|", ";", "**", "-d", "中", "é", `unclosed " prose `, `\" prose `} {
		in := prefix + `{"apiKey":"hunter2"}`
		if got, want := redactString(in), mainOracleRedactString(in); got != want {
			t.Errorf("JSON coverage: got %q want %q", got, want)
		}
	}
	in := `x{"token":"hunter2 secret=abcdef"}`
	if got, want := redactString(in), mainOracleRedactString(in); got != want {
		t.Errorf("whole token: got %q want %q", got, want)
	}
}

func TestRedactionRules(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"pass-context", "first_pass = compute_all(x)", "first_pass = compute_all(x)"},
		{"passphrase", "GPG_PASSPHRASE=hunter2hunter2", "GPG_PASSPHRASE=[REDACTED:secret]"},
		{"pwd", "DB_PWD=hunter2hunter2 ADMIN_PWD=hunter2hunter2", "DB_PWD=[REDACTED:secret] ADMIN_PWD=[REDACTED:secret]"},
		{"whsec-base64", "whs" + "ec_MfKQ9r8+GKYqrTwjUPD8ILPZIo2LaLaSw/=!", "[REDACTED:stripe-webhook]!"},
		{"glpat-dash", "glp" + "at-" + strings.Repeat("a", 19) + "-!", "[REDACTED:gitlab-token]!"},
		{"slack-host", strings.Replace(fakeSlack, "hooks.slack.com", "HOOKS.SLACK.COM", 1), "[REDACTED:slack-webhook]"},
		{"connection-slash", "postgres://admin:ab/cdEF12@db.example.com/app", "[REDACTED:connstring]"},
		{"connection-colon", "redis://:pa:ss@host", "[REDACTED:connstring]"},
		{"connection-last-at", "postgres://u:pa@ss@host/app", "[REDACTED:connstring]"},
		{"order-password", "postgres://u:p@h/db?password=abc,defghij", "[REDACTED:connstring]"},
		{"order-token", "postgres://u:p@h/db?token=abc)defghij", "[REDACTED:connstring]"},
		{"order-basic", "postgres://u:p@h/Authorization: Basic YTpi", "[REDACTED:connstring] Basic [REDACTED:token]"},
		{"aws-glued", "AKIA" + "ABCDEFGHIJKLMNOP" + "secret=abcdefgh", "[REDACTED:aws-key]secret=[REDACTED:secret]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Redact(c.in); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestRedactionGuards(t *testing.T) {
	cases := []string{"BYPASS=some-long-value", "COMPASS=north-west", "ENCOMPASS=some-long-value", "first_pass = compute_all(x)", "RENDER_PASS: shadowmap", "SECOND_PASS=another-round", "PASS_THROUGH=some-long-value", "FIRST_PASS=some-long-value", "TEST_PASS=some-long-value", "NUM_PASS=some-long-value", "PWD=/home/x", "registry.example.com:443@scope", "glp" + "at-" + strings.Repeat("x", 20), "glp" + "at-YOUR_PERSONAL_ACCESS_TOKEN_HERE", "self.h" + "f_modelConfigurationABCDEFGHIJKLMNOPQRSTUVWXYZ", "Authorization: token YOUR_PERSONAL_ACCESS_TOKEN_HERE", "task_s" + "k-learn-model-configuration-example", "task_s" + "k-learn-model-configuration-2026", "https://hooks.slack.com/" + "services/T00000000/B00000000/" + strings.Repeat("X", 24)}
	for i, in := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if got := Redact(in); got != in {
				t.Errorf("guard changed: got %q want %q", got, in)
			}
		})
	}
}

// Map each output marker back to its original input byte interval. The corpus
// uses stable JSON key order; whitespace may normalize when JSON is re-encoded.
// Comparing covered input positions catches partial tails and replacements at
// the wrong location, unlike marker counts or whole-substring-only checks.
func removedPositions(t *testing.T, original, output string) []bool {
	t.Helper()
	// Main can append stray closing brackets from an intermediate marker (the
	// connstring regression). Treat those artifacts as part of its replacement.
	// Corpus secrets are alphanumeric, so this cannot hide retained secret bytes.
	marker := regexp.MustCompile(`\[REDACTED:[a-z-]+\]\]*`)
	var pattern strings.Builder
	pattern.WriteString("(?s)^\\s*")
	pos := 0
	for _, m := range marker.FindAllStringIndex(output, -1) {
		for _, c := range output[pos:m[0]] {
			if !unicode.IsSpace(c) {
				pattern.WriteString(regexp.QuoteMeta(string(c)))
				pattern.WriteString(`\s*`)
			}
		}
		pattern.WriteString("(.*?)")
		pos = m[1]
	}
	for _, c := range output[pos:] {
		if !unicode.IsSpace(c) {
			pattern.WriteString(regexp.QuoteMeta(string(c)))
			pattern.WriteString(`\s*`)
		}
	}
	pattern.WriteString("$")
	matches := regexp.MustCompile(pattern.String()).FindStringSubmatchIndex(original)
	if matches == nil {
		t.Fatalf("cannot map oracle corpus output to original: %q -> %q", original, output)
	}
	covered := make([]bool, len(original))
	for i := 2; i < len(matches); i += 2 {
		for j := matches[i]; j < matches[i+1]; j++ {
			covered[j] = true
		}
	}
	return covered
}

func TestRedactionMainCoverageCorpus(t *testing.T) {
	r := rand.New(rand.NewSource(57))
	prefixes := []string{"result->", "<pre>", "resp|", strings.Repeat("[x ", 128), strings.Repeat("[INFO] request ok\n", 130), "中", "é", `unclosed " `}
	for c := byte(33); c < 127; c++ {
		if c < '0' || c > '9' && c < 'A' || c > 'Z' && c < 'a' || c > 'z' {
			prefixes = append(prefixes, "prose"+string(c))
		}
	}
	for i := 0; i < 12000; i++ {
		secret := fmt.Sprintf("witness%08dZ", i)
		prefix := prefixes[(i/8)%len(prefixes)]
		var noise strings.Builder
		for n := r.Intn(30); n > 0; n-- {
			noise.WriteByte("[]{}\\\" x"[r.Intn(8)])
		}
		var in string
		switch i % 8 {
		case 0:
			in = prefix + `{"apiKey":"` + secret + `"}`
		case 1:
			in = noise.String() + `{"nested":[{"token":"` + secret + `"}]}`
		case 2:
			in = prefix + `{"token":"` + secret + ` secret=abcdef"}`
		case 3:
			in = "postgres://u:p@h/db?password=abc," + secret
		case 4:
			in = "postgres://u:p@h/db?token=abc)" + secret
		case 6:
			in = "postgres://u:p@h/Authorization: Basic " + secret
		case 7:
			in = prefix + `{"nested":[{"password":"` + secret + `"}]}`
		default:
			in = noise.String() + `[{"a":"brackets ] } \\\"","secret":"` + secret + `"},broken]`
		}
		old := mainOracleRedactString(in)
		got := redactString(in)
		oldPositions := removedPositions(t, in, old)
		newPositions := removedPositions(t, in, got)
		for pos, removed := range oldPositions {
			if removed && !newPositions[pos] {
				t.Fatalf("main coverage lost at case %d byte %d: original replaced substring survives", i, pos)
			}
		}
	}
}

func TestRedactionBalancedInvalidNesting(t *testing.T) {
	// A balanced but invalid outer candidate must not repeatedly decode its
	// nested invalid children, or hide a valid JSON sibling/descendant.
	in := strings.Repeat("[", 9000) + `{"token":"hunter2"},broken` + strings.Repeat("]", 9000)
	start := time.Now()
	got := redactString(in)
	if strings.Contains(got, "hunter2") {
		t.Fatal("valid descendant hidden by invalid balanced parent")
	}
	if time.Since(start) > linearBudget {
		t.Fatal("balanced invalid nesting exceeded 500ms")
	}
}

func TestRedactionCredentialKeys(t *testing.T) {
	for _, context := range []string{"DB", "MYSQL", "SMTP", "REDIS", "ADMIN", "ROOT", "USER", "MAIL", "FTP", "SSH", "LDAP", "PG", "POSTGRES"} {
		in := context + "_PASS=hunter2hunter2"
		if got, want := Redact(in), context+"_PASS=[REDACTED:secret]"; got != want {
			t.Errorf("credential context: got %q want %q", got, want)
		}
	}
	for _, key := range []string{"GPG_PASSPHRASE", "DB_PWD", "ADMIN_PWD", "DB_PASS"} {
		in := `{"` + key + `":"hunter2"}`
		if got, want := redactString(in), `{"`+key+`":"[REDACTED:key]"}`; got != want {
			t.Errorf("structured credential key: got %q want %q", got, want)
		}
	}
	for _, key := range []string{"PWD", "first_pass", "RENDER_PASS"} {
		in := `{"` + key + `":"/home/x"}`
		if got := redactString(in); got != in {
			t.Errorf("structured key guard: got %q want %q", got, in)
		}
	}
}

func TestRedactionMainCoverageGluedTokens(t *testing.T) {
	tokens := []string{"AKIA" + "ABCDEFGHIJKLMNOP", fakeAKIA, fakeGHP, fakeSK, "github_" + "pat_" + strings.Repeat("a", 30), "glp" + "at-" + strings.Repeat("a", 24), "np" + "m_" + strings.Repeat("a", 36), "h" + "f_" + strings.Repeat("a", 34), fakeSlack, "whs" + "ec_" + strings.Repeat("a", 24), "Authorization: token " + strings.Repeat("a", 24), "x_" + fakeSK, "-----BEGIN PGP PRIVATE KEY BLOCK-----\nabc\n-----END PGP PRIVATE KEY BLOCK-----", "password=abcdefgh", "Bearer " + strings.Repeat("a", 24), "xox" + "b-" + strings.Repeat("a", 24), "sk_" + "live_" + strings.Repeat("a", 24), "AIza" + strings.Repeat("a", 35), "eyJ" + strings.Repeat("a", 10) + "." + strings.Repeat("a", 10) + "." + strings.Repeat("a", 10), "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----"}
	for _, n := range []int{20, 34, 36, 64, 128} {
		for _, prefix := range []string{"glp" + "at-", "np" + "m_", "h" + "f_", "whs" + "ec_", "Authorization: token ", "x_s" + "k-"} {
			tokens = append(tokens, prefix+strings.Repeat("A", n))
		}
	}
	keys := []string{"secret=abcdefgh", "API_KEY=abcdefgh", "password: abcdefgh", "DB_PASS=abcdefgh", "DB_PWD=abcdefgh", "MY_DB_PASS=abcdefgh", "APP_ROOT_PWD=abcdefgh", "PASSPHRASE=abcdefgh", "Authorization: Bearer " + strings.Repeat("b", 24), "Proxy-Authorization: token " + strings.Repeat("b", 24), "Authorization: Basic dXNlcjpwYXNz", "Proxy-Authorization: Basic dXNlcjpwYXNz", "Cookie: session=abcdefgh", "Set-Cookie: session=abcdefgh", "Authorization: token " + strings.Repeat("b", 24)}
	for _, token := range tokens {
		for _, key := range keys {
			for _, sep := range []string{"", " ", ";", "\n"} {
				in := token + sep + key
				old, got := removedPositions(t, in, mainOracleRedactString(in)), removedPositions(t, in, redactString(in))
				for i, removed := range old {
					if removed && !got[i] {
						t.Errorf("glued coverage lost at byte %d: %q -> %q", i, in, redactString(in))
						break
					}
				}
			}
		}
	}
}
