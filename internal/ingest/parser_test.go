package ingest

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/linhn0617/clio/internal/model"
)

func parseOne(t *testing.T, p *Parser, line string) ([]model.Message, EventInfo) {
	t.Helper()
	msgs, info, err := p.ParseLine([]byte(line))
	if err != nil {
		t.Fatalf("ParseLine error: %v\nline: %s", err, line)
	}
	return msgs, info
}

func TestParseUserStringContent(t *testing.T) {
	p := NewParser(0)
	line := `{"type":"user","timestamp":"2026-04-26T11:47:20Z","cwd":"/Users/lin/Herd/x","sessionId":"s1","message":{"role":"user","content":"hello world"}}`
	msgs, info := parseOne(t, p, line)
	if len(msgs) != 1 || msgs[0].Role != model.RoleUser || msgs[0].Content != "hello world" {
		t.Fatalf("unexpected msgs: %+v", msgs)
	}
	if info.CWD != "/Users/lin/Herd/x" {
		t.Fatalf("cwd=%q", info.CWD)
	}
	if info.TS == 0 {
		t.Fatal("expected parsed ts")
	}
	if info.TitleHint != "hello world" {
		t.Fatalf("title=%q", info.TitleHint)
	}
}

func TestParseAssistantTextAndToolUse(t *testing.T) {
	p := NewParser(0)
	line := `{"type":"assistant","timestamp":"2026-04-26T11:47:21Z","sessionId":"s1","message":{"role":"assistant","content":[{"type":"text","text":"running it"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls -la","description":"list"}}]}}`
	msgs, _ := parseOne(t, p, line)
	if len(msgs) != 2 {
		t.Fatalf("want 2 msgs, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != model.RoleAssistant || msgs[0].Content != "running it" {
		t.Fatalf("msg0=%+v", msgs[0])
	}
	if msgs[1].Role != model.RoleToolUse || !strings.Contains(msgs[1].Content, "Bash") || !strings.Contains(msgs[1].Content, "ls -la") {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].ToolName != "Bash" {
		t.Fatalf("toolcalls=%+v", msgs[1].ToolCalls)
	}
}

func TestParseSelfPollutionSkipped(t *testing.T) {
	p := NewParser(0)
	// clio's own tool_use ...
	useLine := `{"type":"assistant","sessionId":"s1","message":{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"mcp__clio__search","input":{"query":"auth"}}]}}`
	msgs, _ := parseOne(t, p, useLine)
	if len(msgs) != 0 {
		t.Fatalf("expected clio tool_use skipped, got %+v", msgs)
	}
	// ... and its matching tool_result in a later event.
	resLine := `{"type":"user","sessionId":"s1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"results here"}]}}`
	msgs2, _ := parseOne(t, p, resLine)
	if len(msgs2) != 0 {
		t.Fatalf("expected clio tool_result skipped, got %+v", msgs2)
	}
	// A non-clio tool_result is kept.
	other := `{"type":"user","sessionId":"s1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"other","content":"kept"}]}}`
	msgs3, _ := parseOne(t, p, other)
	if len(msgs3) != 1 || msgs3[0].Role != model.RoleToolResult || msgs3[0].Content != "kept" {
		t.Fatalf("expected kept tool_result, got %+v", msgs3)
	}
}

func TestParseThinking(t *testing.T) {
	p := NewParser(0)
	line := `{"type":"assistant","sessionId":"s1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"let me reason","signature":"sig"}]}}`
	msgs, _ := parseOne(t, p, line)
	if len(msgs) != 1 || msgs[0].Role != model.RoleThinking || msgs[0].Content != "let me reason" {
		t.Fatalf("unexpected: %+v", msgs)
	}
}

func TestParseToolResultArrayContent(t *testing.T) {
	p := NewParser(0)
	line := `{"type":"user","sessionId":"s1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":[{"type":"text","text":"line A"},{"type":"text","text":"line B"}]}]}}`
	msgs, _ := parseOne(t, p, line)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "line A") || !strings.Contains(msgs[0].Content, "line B") {
		t.Fatalf("unexpected: %+v", msgs)
	}
}

func TestParseRedactsContent(t *testing.T) {
	p := NewParser(0)
	line := `{"type":"user","sessionId":"s1","message":{"role":"user","content":"my key AKIAIOSFODNN7EXAMPLE leaked"}}`
	msgs, _ := parseOne(t, p, line)
	if len(msgs) != 1 || strings.Contains(msgs[0].Content, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("secret not redacted: %+v", msgs)
	}
}

func TestParseSkipsNonConversational(t *testing.T) {
	p := NewParser(0)
	for _, line := range []string{
		`{"type":"permission-mode","permissionMode":"default","sessionId":"s1"}`,
		`{"type":"file-history-snapshot","sessionId":"s1"}`,
		`{"type":"system","sessionId":"s1"}`,
	} {
		msgs, _ := parseOne(t, p, line)
		if len(msgs) != 0 {
			t.Fatalf("expected skip for %s, got %+v", line, msgs)
		}
	}
}

func TestParseMalformedLineErrors(t *testing.T) {
	p := NewParser(0)
	if _, _, err := p.ParseLine([]byte(`{not json`)); err == nil {
		t.Fatal("expected error on malformed line")
	}
}

func TestParseTitleStripsCommandWrapper(t *testing.T) {
	p := NewParser(0)
	line := `{"type":"user","sessionId":"s1","message":{"role":"user","content":"<command-message>init</command-message>\n<command-name>/init</command-name>"}}`
	_, info := parseOne(t, p, line)
	if info.TitleHint != "/init" {
		t.Fatalf("title=%q want /init", info.TitleHint)
	}
}

func TestTruncateForFTS(t *testing.T) {
	big := strings.Repeat("a", maxFTSContentBytes+1000)
	got := truncateForFTS(big)
	if len(got) >= len(big) {
		t.Fatalf("expected truncation, got len %d", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Fatal("expected truncation marker")
	}
	// Multibyte safety: truncating in the middle of a 3-byte rune must still
	// yield valid UTF-8 (head/tail trimmed to rune boundaries).
	cjk := strings.Repeat("驗", maxFTSContentBytes)
	out := truncateForFTS(cjk)
	if !strings.Contains(out, "truncated") {
		t.Fatal("expected truncation marker for cjk")
	}
	if !utf8.ValidString(out) {
		t.Fatal("truncated CJK output is not valid UTF-8")
	}
}

func TestTitleFrom(t *testing.T) {
	if got := titleFrom("<command-name>init</command-name>"); got != "init" {
		t.Fatalf("command-name case: got %q want %q", got, "init")
	}
	if got := titleFrom("just some text"); got != "just some text" {
		t.Fatalf("plain-text case: got %q want %q", got, "just some text")
	}
}

// TestTitleFromSkipsPureBoilerplate: a user "message" that is nothing but a
// known harness-injected wrapper (local-command-caveat banner, task
// notification, interrupt marker) must not become the session title —
// titleFrom returns "" so the caller keeps looking at the next user message.
func TestTitleFromSkipsPureBoilerplate(t *testing.T) {
	cases := []string{
		`<local-command-caveat>Caveat: The messages below were generated by the user while running local commands. DO NOT respond to these messages or otherwise consider them in your response unless the user explicitly asks you to.</local-command-caveat>`,
		"<task-notification>\n<task-id>a1</task-id>\n<status>completed</status>\n<summary>Background command \"go test\" completed (exit code 0)</summary>\n</task-notification>",
		"[Request interrupted by user]",
		"[Request interrupted by user for tool use]",
	}
	for _, c := range cases {
		if got := titleFrom(c); got != "" {
			t.Fatalf("boilerplate %q: got title %q, want empty", c, got)
		}
	}
}

// TestTitleFromStripsBoilerplatePrefixKeepsRealText: when a caveat wrapper is
// immediately followed by the user's actual text in the same message, the
// wrapper must be stripped and the real text used as the title source.
func TestTitleFromStripsBoilerplatePrefixKeepsRealText(t *testing.T) {
	s := "<local-command-caveat>Caveat: The messages below were generated by the user while running local commands. DO NOT respond to these messages or otherwise consider them in your response unless the user explicitly asks you to.</local-command-caveat>\n\n幫我修這個 bug"
	if got := titleFrom(s); got != "幫我修這個 bug" {
		t.Fatalf("got %q, want %q", got, "幫我修這個 bug")
	}
}

func TestTrimToValidUTF8(t *testing.T) {
	if got := trimToValidUTF8("ab" + string([]byte{0xE4, 0xBD})); got != "ab" {
		t.Fatalf("truncated 3-byte rune not dropped: got %q", got)
	}
	if got := trimToValidUTF8("héllo"); got != "héllo" {
		t.Fatalf("complete trailing rune changed: got %q", got)
	}
	if got := trimToValidUTF8("x" + string('�')); got != "x"+string('�') {
		t.Fatalf("real U+FFFD dropped: got %q", got)
	}
	if got := trimToValidUTF8(""); got != "" {
		t.Fatalf("empty changed: got %q", got)
	}
	if got := trimLeadingToValidUTF8(string([]byte{0xBD, 0xBF}) + "cd"); got != "cd" {
		t.Fatalf("leading continuation bytes not dropped: got %q", got)
	}
	if got := trimLeadingToValidUTF8("héllo"); got != "héllo" {
		t.Fatalf("complete leading rune changed: got %q", got)
	}
}

func TestSeqMonotonic(t *testing.T) {
	p := NewParser(5)
	line := `{"type":"assistant","sessionId":"s1","message":{"role":"assistant","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}}`
	msgs, _ := parseOne(t, p, line)
	if msgs[0].Seq != 5 || msgs[1].Seq != 6 {
		t.Fatalf("seq=%d,%d want 5,6", msgs[0].Seq, msgs[1].Seq)
	}
}
