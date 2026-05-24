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

// Task 1.1 / 1.2: connection-string rule
func TestRedactConnectionString(t *testing.T) {
	redacted := []string{
		"postgres://user:pass@host/db",
		"redis://h:p@x",
		"mysql://admin:s3cret@db.example.com:3306/mydb",
	}
	preserved := []string{
		"https://example.com",
		"https://user@host", // no password
		"http://localhost:8080",
	}
	for _, s := range redacted {
		got := Redact(s)
		if !strings.Contains(got, "[REDACTED:connstring]") {
			t.Errorf("expected connstring redaction for %q, got %q", s, got)
		}
	}
	for _, s := range preserved {
		got := Redact(s)
		if strings.Contains(got, "[REDACTED") {
			t.Errorf("expected %q preserved, got %q", s, got)
		}
	}
}

// Task 1.3 / 1.4: isSecretKey
func TestIsSecretKey(t *testing.T) {
	trueKeys := []string{
		"apiKey", "API_KEY", "token", "db_password", "access_key",
		"secret_key", "auth_token", "credentials", "dsn",
		"password", "passwd", "secret", "secrets", "credential",
		"apikey", "accesskey", "access_key", "privatekey", "private_key",
		"connection_string", "conn_str",
	}
	falseKeys := []string{
		"url", "name", "id", "title", "author",
		"tokenizer", "oauth_provider", "publicKey",
	}
	for _, k := range trueKeys {
		if !isSecretKey(k) {
			t.Errorf("isSecretKey(%q) = false, want true", k)
		}
	}
	for _, k := range falseKeys {
		if isSecretKey(k) {
			t.Errorf("isSecretKey(%q) = true, want false", k)
		}
	}
}

// Task 2.1: redactString
func TestRedactString(t *testing.T) {
	// plain text with openai-style key: regex redaction
	got := redactString("my key is sk-aaaaaaaaaaaaaaaaaaaa here")
	if !strings.Contains(got, "[REDACTED") {
		t.Errorf("expected regex redaction in plain text, got %q", got)
	}

	// JSON-as-text value: structural redaction on secret key
	got = redactString(`{"apiKey":"secret-value-123456"}`)
	if strings.Contains(got, "secret-value-123456") {
		t.Errorf("secret value leaked in JSON-as-text: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:key]") {
		t.Errorf("expected [REDACTED:key] in JSON-as-text, got %q", got)
	}

	// plain prose: unchanged
	got = redactString("hello world, this is fine")
	if got != "hello world, this is fine" {
		t.Errorf("plain prose changed: %q", got)
	}

	// CJK text: unchanged
	got = redactString("驗證流程")
	if got != "驗證流程" {
		t.Errorf("CJK text changed: %q", got)
	}
}

// Task 2.2: redactJSON
func TestRedactJSON(t *testing.T) {
	// nested secret keys: all secret values redacted, benign preserved
	input := `{"apiKey":"x123456","note":"hi","nested":{"token":"abc123456"}}`
	got := string(redactJSON([]byte(input)))
	if strings.Contains(got, "x123456") {
		t.Errorf("apiKey value leaked: %q", got)
	}
	if strings.Contains(got, "abc123456") {
		t.Errorf("nested token value leaked: %q", got)
	}
	if !strings.Contains(got, "hi") {
		t.Errorf("benign note value not preserved: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:key]") {
		t.Errorf("expected [REDACTED:key] markers: %q", got)
	}

	// whole-subtree: number value
	got = string(redactJSON([]byte(`{"token":123}`)))
	if strings.Contains(got, "123") {
		t.Errorf("token number value leaked: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:key]") {
		t.Errorf("expected [REDACTED:key] for number token: %q", got)
	}

	// whole-subtree: object value
	got = string(redactJSON([]byte(`{"auth_token":{"u":"a","p":"b"}}`)))
	if strings.Contains(got, `"u"`) || strings.Contains(got, `"a"`) {
		t.Errorf("auth_token object subtree leaked: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:key]") {
		t.Errorf("expected [REDACTED:key] for object subtree: %q", got)
	}

	// whole-subtree: array value
	got = string(redactJSON([]byte(`{"credential":["x","y"]}`)))
	if strings.Contains(got, `"x"`) || strings.Contains(got, `"y"`) {
		t.Errorf("credential array leaked: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:key]") {
		t.Errorf("expected [REDACTED:key] for array credential: %q", got)
	}

	// benign key with shape secret in value: regex redacts via redactString
	got = string(redactJSON([]byte(`{"msg":"sk-aaaaaaaaaaaaaaaaaaaa"}`)))
	if strings.Contains(got, "sk-aaaaaaaaaaaaaaaaaaaa") {
		t.Errorf("sk-key leaked in benign-key field: %q", got)
	}

	// benign url preserved
	got = string(redactJSON([]byte(`{"url":"https://example.com"}`)))
	if !strings.Contains(got, "https://example.com") {
		t.Errorf("url not preserved: %q", got)
	}

	// <, >, & must NOT be unicode-escaped; SetEscapeHTML(false) keeps them literal.
	// Default json.Encoder escapes to \u003c / \u003e / \u0026.
	got = string(redactJSON([]byte(`{"msg":"<hello> & <world>"}`)))
	if strings.Contains(got, `\u003c`) || strings.Contains(got, `\u003e`) || strings.Contains(got, `\u0026`) {
		t.Errorf("HTML chars were unicode-escaped (want SetEscapeHTML(false)): %q", got)
	}
	if !strings.Contains(got, "<hello>") {
		t.Errorf("<hello> not preserved: %q", got)
	}

	// large integer preserved (UseNumber)
	got = string(redactJSON([]byte(`{"id":123456789012345678}`)))
	if !strings.Contains(got, "123456789012345678") {
		t.Errorf("large int not preserved: %q", got)
	}

	// non-JSON line: fallback to text Redact (no panic, still scrubbed for shape secrets)
	nonJSON := `not json at all AKIAIOSFODNN7EXAMPLE`
	got = string(redactJSON([]byte(nonJSON)))
	if strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("non-JSON fallback didn't redact: %q", got)
	}

	// deep nesting + arrays of objects
	deep := `{"outer":{"arr":[{"token":"deep-secret"},{"name":"alice"}]}}`
	got = string(redactJSON([]byte(deep)))
	if strings.Contains(got, "deep-secret") {
		t.Errorf("deep nested token leaked: %q", got)
	}
	if !strings.Contains(got, "alice") {
		t.Errorf("benign name not preserved in deep: %q", got)
	}

	// CJK values intact
	got = string(redactJSON([]byte(`{"note":"驗證流程","name":"张三"}`)))
	if !strings.Contains(got, "驗證流程") {
		t.Errorf("CJK value not preserved: %q", got)
	}
	if !strings.Contains(got, "张三") {
		t.Errorf("CJK name not preserved: %q", got)
	}
}
