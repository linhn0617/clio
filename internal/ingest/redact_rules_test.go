package ingest

import (
	"strings"
	"testing"
)

// Synthetic credentials only: shapes match the rules, none is a live secret.
const (
	fakeSK   = "sk-" + "abcdefghijklmnopqrstuvwxyz0123"
	fakeAKIA = "AKIA" + "IOSFODNN7EXAMPLE"
	fakeGHP  = "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789"
	// Split so the repository secret scanner (push protection) does not read the test fixture as a real webhook.
	fakeSlack = "https://hooks.slack.com/" + "services/T01234567/" + "B01234567/" + "abcdefghijklmnopqrstuvwx"
)

// New low-false-positive rules: each case is (input, exact expected output).
func TestRedactNewRulesPositive(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"gitlab pat", "token glp" + "at-abcdefghij0123456789 here", "token [REDACTED:gitlab-token] here"},
		{"gitlab pat with dash and underscore", "glp" + "at-abc_def-ghij0123456789", "[REDACTED:gitlab-token]"},
		{"npm token", "//registry.npmjs.org/:_authToken=np" + "m_abcdefghijklmnopqrstuvwxyz0123456789", "//registry.npmjs.org/:_authToken=[REDACTED:secret]"},
		{"npm token bare", "use np" + "m_abcdefghijklmnopqrstuvwxyz0123456789 to publish", "use [REDACTED:npm-token] to publish"},
		{"hugging face token", "export x=h" + "f_abcdefghijklmnopqrstuvwxyzABCDEFGH done", "export x=[REDACTED:hf-token] done"},
		{"stripe webhook secret", "whs" + "ec_abcdefghijklmnopqrstuvwxyz012345", "[REDACTED:stripe-webhook]"},
		{"slack incoming webhook", "post to " + fakeSlack + " now", "post to [REDACTED:slack-webhook] now"},
		{"pgp private key block", "a\n-----BEGIN PGP PRIVATE KEY BLOCK-----\nlQdGBF\nabc=\n-----END PGP PRIVATE KEY BLOCK-----\nb", "a\n[REDACTED:private-key]\nb"},
		{"authorization token (legacy github)", "Authorization: token 0123456789abcdef0123456789abcdef01234567", "Authorization: token [REDACTED:token]"},
		{"proxy-authorization token, any case", "proxy-AUTHORIZATION:  Token 0123456789abcdef0123456789abcdef01234567", "proxy-AUTHORIZATION:  Token [REDACTED:token]"},
		{"connstring with empty user", "redis://:s3cretpass@cache.example.com:6379/0", "[REDACTED:connstring]"},
		{"mysql_pwd", "MYSQL_PWD=hunter2hunter2 mysql -u root", "MYSQL_PWD=[REDACTED:secret] mysql -u root"},
		{"db_pass", "DB_PASS=hunter2hunter2", "DB_PASS=[REDACTED:secret]"},
		{"suffix _PASS with colon", "REDIS_PASS: hunter2hunter2", "REDIS_PASS:[REDACTED:secret]"},
		{"lower-case quoted _pass", "db_pass='hunter2hunter2'", "db_pass=[REDACTED:secret]"},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Errorf("%s: Redact(%q)\n got  %q\n want %q", c.name, c.in, got, c.want)
		}
	}
}

// Ordinary text that merely resembles the new shapes must pass through unchanged
// (over-redaction destroys searchability).
func TestRedactNewRulesGuards(t *testing.T) {
	untouched := []string{
		"the glpat- prefix marks GitLab tokens", "glpat-short", "glpat-",
		"npm_package_name and npm_config_registry=https://registry.npmjs.org/",
		"npm_short", "npm_" + strings.Repeat("a", 20),
		"hf_hub_download(repo_id)", "hf_short", "see hf_ prefix",
		"whsec_", "whsec_short", "the whsec_ prefix",
		"https://hooks.slack.com/services/", "https://hooks.slack.com/services/TXXX/BXXX/xxxx",
		"https://slack.com/help/articles/115005265063",
		"-----BEGIN PGP PUBLIC KEY BLOCK-----\nabc\n-----END PGP PUBLIC KEY BLOCK-----",
		"-----BEGIN PGP MESSAGE-----\nabc\n-----END PGP MESSAGE-----",
		"a PGP PRIVATE KEY BLOCK is exported with gpg --export-secret-keys",
		"Authorization: token required for this endpoint", "Authorization: token",
		"the Authorization: token scheme is deprecated",
		"redis://cache.example.com:6379/0", "http://localhost:8080", "http://:8080/x@y",
		"https://example.com:8080/a@b", "redis://:@host", "see https://example.com/path?q=1",
		"PWD=/Users/lin/project", "OLDPWD=/Users/lin/other-directory",
		"BYPASS_CACHE=enabled-for-all", "ENABLE_PASS_THROUGH=yes-please", "COMPASS=northwest-ish",
		"BY_PASS_ROUTE=some-route-value", "the pass is long enough here",
		"we pass: everything-went-fine is not a variable",
		"a normal sentence mentioning a token and a password policy",
	}
	for _, s := range untouched {
		if got := Redact(s); got != s {
			t.Errorf("guard: Redact(%q) = %q, want unchanged", s, got)
		}
	}
}

// (a) a connection string whose password is itself another rule's secret shape
// must collapse to exactly one marker; before the fix the earlier rule's marker
// was swallowed by the connstring match, leaving a stray ']'.
func TestRedactConnstringWithNestedSecretShape(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a://u:p@" + fakeSK, "[REDACTED:connstring]"},
		{"a://u:p@" + fakeGHP, "[REDACTED:connstring]"},
		{"postgres://u:" + fakeAKIA + "@h/db", "[REDACTED:connstring]"},
		{"x https://" + fakeGHP + ":x-oauth-basic@github.com/o/r.git y", "x [REDACTED:connstring] y"},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Errorf("Redact(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}

// (b) sk- right after an underscore is a "prefix_secret" concatenation, not the
// tail of an English word, so it is redacted; letters and digits before sk- are
// word material (risk-, disk-, task-) and stay.
func TestRedactSkAfterUnderscore(t *testing.T) {
	if got, want := Redact("x_"+fakeSK), "x_[REDACTED:api-key]"; got != want {
		t.Errorf("underscore-prefixed key\n got  %q\n want %q", got, want)
	}
	if got, want := Redact("OPENAI__"+fakeSK+" ok"), "OPENAI__[REDACTED:api-key] ok"; got != want {
		t.Errorf("double underscore\n got  %q\n want %q", got, want)
	}
	for _, s := range []string{
		"risk-assessment-for-the-quarterly-report-2026",
		"disk-usage-statistics-for-all-mounted-volumes",
		"abc9" + fakeSK, "task" + fakeSK, "Xs" + "k-abcdefghijklmnopqrstuvwxyz0123",
	} {
		if got := Redact(s); got != s {
			t.Errorf("guard: Redact(%q) = %q, want unchanged", s, got)
		}
	}
}

// (c) AWS keys in secret-named values were already redacted on main. The
// adjacent-key-name regression (AKIA…secret=…) is covered by TestRedactionRules.
func TestRedactAWSKeyInsideSecretValue(t *testing.T) {
	for _, w := range []string{
		"secret=%s", "secret: %s", "SECRET_KEY=%s", `secret="%s"`, "secret='%s'", "token=%s,",
		"x_secret=%s", "secret = %s", "secret=%s%s", "password=pre%s", "secret=%s.", "secret=(%s)",
		"secret=_%s", "secret==%s", "secret=[%s]", "secret={%s}", "secret=%s x", `secret="a b %s"`,
		"Cookie: a=%s", "Bearer %s", "postgres://u:%s@h", "http://u:p@%s", "api_key=%s\nother",
	} {
		in := strings.ReplaceAll(w, "%s", fakeAKIA)
		if got := Redact(in); strings.Contains(got, fakeAKIA) {
			t.Errorf("AWS key id leaked: Redact(%q) = %q", in, got)
		}
	}
}

// (d) Go's \b is ASCII-only, so a key that starts with U+017F LONG S (which
// (?i) folds onto "s") had no boundary at the start of text or after a space.
func TestRedactSecretKeyStartingWithLongS(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\u017fecret=xxxxxx", "\u017fecret=[REDACTED:secret]"},
		{" \u017fecret=xxxxxx", " \u017fecret=[REDACTED:secret]"},
		{"a \u017fecret: xxxxxx", "a \u017fecret:[REDACTED:secret]"},
		{"x\n\u017fecret=xxxxxx", "x\n\u017fecret=[REDACTED:secret]"},
		{"\u212a_TOKEN=xxxxxx", "\u212a_TOKEN=[REDACTED:secret]"},
		{"A_TOKEN='aaaaaaaa'B_TOKEN=bbbbbbbb", "A_TOKEN=[REDACTED:secret]B_TOKEN=[REDACTED:secret]"},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Errorf("Redact(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
	}
	for _, s := range []string{"the \u017fecret garden", "\u017fecret=short"} {
		if got := Redact(s); got != s {
			t.Errorf("guard: Redact(%q) = %q, want unchanged", s, got)
		}
	}
}

// The prefilter must not change what the new rules redact: every positive and
// guard shape above, plus the long-s and underscore edge forms.
func TestPrefilterEquivalentNewRules(t *testing.T) {
	for _, s := range []string{
		"glp" + "at-abcdefghij0123456789", "np" + "m_abcdefghijklmnopqrstuvwxyz0123456789", "h" + "f_abcdefghijklmnopqrstuvwxyzABCDEFGH",
		"whs" + "ec_abcdefghijklmnopqrstuvwxyz012345", fakeSlack, "HTTPS://HOOKS.SLACK.COM/SERVICES/T0/B0/abcdefghijklmnopqrstuvwx",
		"-----BEGIN PGP PRIVATE KEY BLOCK-----\nabc\n-----END PGP PRIVATE KEY BLOCK-----",
		"Authorization: token 0123456789abcdef0123456789abcdef01234567", "AUTHORIZATION:TOKEN 0123456789abcdef0123456789abcdef01234567",
		"redis://:s3cretpass@cache.example.com:6379/0", "MYSQL_PWD=hunter2hunter2", "DB_PASS=hunter2hunter2", "db_pass: hunter2hunter2",
		"x_" + fakeSK, "a://u:p@" + fakeSK, "\u017fecret=xxxxxx", " \u017fECRET: xxxxxx", "\u212a_TOKEN=xxxxxx", "\u017f_PASS=xxxxxx",
		"PWD=/Users/lin/project", "BYPASS_CACHE=enabled-for-all", "Authorization: token required for this endpoint",
	} {
		checkSame(t, s)
	}
}
