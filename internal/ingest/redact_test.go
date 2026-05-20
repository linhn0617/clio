package ingest

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		redacted  bool   // expect a [REDACTED marker
		keepToken string // substring that must survive (e.g. key name)
	}{
		{"aws key", "key is AKIAIOSFODNN7EXAMPLE here", true, ""},
		{"gcp key", "AIza01234567890123456789012345678901234", true, ""},
		{"github token", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", true, ""},
		{"openai key", "sk-abcdefghijklmnopqrstuvwxyz0123", true, ""},
		{"bearer", "Authorization: Bearer abcdef123456ghijkl", true, ""},
		{"env secret", "API_KEY=supersecretvalue123", true, "API_KEY"},
		{"password assign", "DB_PASSWORD: hunter2hunter2", true, "DB_PASSWORD"},
		{"private key block", "-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY-----", true, ""},
		{"normal text untouched", "let's discuss the validation flow for users", false, "validation flow"},
		{"short value not redacted", "X=1", false, "X=1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Redact(c.in)
			has := strings.Contains(got, "[REDACTED")
			if has != c.redacted {
				t.Fatalf("redacted=%v, want %v\nin:  %q\nout: %q", has, c.redacted, c.in, got)
			}
			if c.keepToken != "" && !strings.Contains(got, c.keepToken) {
				t.Fatalf("expected %q to survive redaction, got %q", c.keepToken, got)
			}
		})
	}
}
