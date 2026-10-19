package ingest

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestReplacementOriginalContext(t *testing.T) {
	for _, in := range []string{"cacheſ://u:pw@h", "cacheK://u:pw@h"} {
		if got, want := Redact(in), mainOracleRedact(in); got != want {
			t.Errorf("original context: got %q want %q", got, want)
		}
	}
	// Force a match at the ASCII/non-ASCII boundary, independently of greediness
	// in the full scheme pattern. Re-matching this slice loses that boundary.
	rule := redactRule{re: regexp.MustCompile(`(?i)\bs://u:pw@h`), repl: "[REDACTED:connstring]", keepMatch: func(string) bool { return false }}
	if got := rule.replace("cacheſ://u:pw@h"); got != "cache[REDACTED:connstring]" {
		t.Errorf("original boundary lost: %q", got)
	}

}
func TestWholeTokenPlaceholder(t *testing.T) {
	for _, body := range []string{"AbCdEfGhIjKlMnOpQr-x", "AbCdEfGhIjKlMnOpQr-YOUR_TOKEN_HERE"} {
		in := "glp" + "at-" + body
		if got := Redact(in); got != "[REDACTED:gitlab-token]" {
			t.Errorf("whole body leaked: %q", got)
		}
	}
	for _, body := range []string{strings.Repeat("x", 20), "XX_xx-XX_xx-XX_xx-XX_xx", "YOUR_PERSONAL_ACCESS_TOKEN_HERE"} {
		in := "glp" + "at-" + body
		if got := Redact(in); got != in {
			t.Errorf("placeholder changed: %q", got)
		}
	}
}
func TestPrefixedCredentialContexts(t *testing.T) {
	for _, key := range []string{"MY_DB_PASS", "APP_ROOT_PASS", "MY_DB_PWD"} {
		in := key + "=hunter2hunter2"
		if got := Redact(in); got != key+"=[REDACTED:secret]" {
			t.Errorf("prefixed credential leaked: %q", got)
		}
		in = `{"` + key + `":"hunter2hunter2"}`
		if got := redactString(in); got != `{"`+key+`":"[REDACTED:key]"}` {
			t.Errorf("structured credential leaked: %q", got)
		}
	}
}
func TestConnectionAuthorityBoundaries(t *testing.T) {
	for _, in := range []string{"curl http://web:3000/users/@me", "http://registry:4873/@scope/pkg", "http://[::1]:8080/a@b", "http://example.com:8080/a@b", "http://localhost:3000/users/@me"} {
		if got := Redact(in); got != in {
			t.Errorf("port path changed: %q -> %q", in, got)
		}
	}
	for _, in := range []string{"redis://:123456@host", "postgres://admin:ab/cdEF12@db.example.com/app", "redis://:pa:ss@host", "http://localhost:3000@v2"} {
		if got := Redact(in); got != "[REDACTED:connstring]" {
			t.Errorf("credential leaked: %q", got)
		}
	}
}
func TestAWSKeyTermination(t *testing.T) {
	for _, in := range []string{"AKIA" + strings.Repeat("X", 20) + "_PREFIX", "AKIA" + "IOSFODNN7EXAMPLE1234"} {
		if got := Redact(in); got != in {
			t.Errorf("overlong AWS changed: %q", got)
		}
	}
	in := "AKIA" + "ABCDEFGHIJKLMNOP" + "secret=abcdefgh"
	if got := Redact(in); got != "[REDACTED:aws-key]secret=[REDACTED:secret]" {
		t.Errorf("glued AWS leaked: %q", got)
	}
}
func TestWorkingDirectoryKeys(t *testing.T) {
	for _, in := range []string{"OLD_PWD=/home/other", "PROJECT_PWD=/Users/x/app", `{"OLD_PWD":"/tmp/x"}`} {
		if got := redactString(in); got != in {
			t.Errorf("working directory changed: %q -> %q", in, got)
		}
	}
}

func BenchmarkBalancedJSONSparseText(b *testing.B) {
	for _, size := range []int{1024, 5 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			in := strings.Repeat("a", size) + "["
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				balancedJSONSpans(in)
			}
		})
	}
}

func TestUnderscoreWordGuardRetainsStandaloneTokenCoverage(t *testing.T) {
	in := "task_s" + "k-learn-" + fakeSK
	old, got := removedPositions(t, in, mainOracleRedactString(in)), removedPositions(t, in, redactString(in))
	for pos, removed := range old {
		if removed && !got[pos] {
			t.Fatalf("standalone token hidden by word guard at byte %d", pos)
		}
	}
}

func TestTokenGluedToConnectionCoverage(t *testing.T) {
	for _, in := range []string{
		"glp" + "at-" + strings.Repeat("a", 24) + "redis://u:pw@host",
		"whs" + "ec_" + strings.Repeat("a", 24) + "/redis://u:pw@host",
		"Authorization: token " + strings.Repeat("a", 24) + "redis://u:pw@host",
		"PASSPHRASE://u:pw@host",
	} {
		old, got := removedPositions(t, in, mainOracleRedactString(in)), removedPositions(t, in, redactString(in))
		for pos, removed := range old {
			if removed && !got[pos] {
				t.Errorf("connection coverage lost at byte %d: %q", pos, redactString(in))
				break
			}
		}
	}
}
