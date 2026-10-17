package termsafe

import "testing"

func TestStrip(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello world", "hello world"},
		{"cjk and emoji", "你好 🙂 世界", "你好 🙂 世界"},
		{"keeps newline and tab", "a\n\tb", "a\n\tb"},
		{"drops carriage return", "a\r\nb\rc", "a\nbc"},
		{"csi clear screen", "x\x1b[2Jy", "xy"},
		{"csi sgr colour", "\x1b[1;31mred\x1b[0m", "red"},
		{"csi with intermediate bytes", "a\x1b[?25lb", "ab"},
		{"osc 52 clipboard bel", "a\x1b]52;c;SGVsbG8=\x07b", "ab"},
		{"osc title st", "a\x1b]0;evil title\x1b\\b", "ab"},
		{"dcs", "a\x1bPq#0;2;0;0;0\x1b\\b", "ab"},
		{"apc", "a\x1b_payload\x1b\\b", "ab"},
		{"pm", "a\x1b^payload\x1b\\b", "ab"},
		{"sos", "a\x1bXpayload\x1b\\b", "ab"},
		{"two byte esc", "a\x1bcb\x1b7c", "abc"},
		{"c0 controls", "a\x00\x01\x07\x08\x0b\x0c\x0e\x1fb", "ab"},
		{"del", "a\x7fb", "ab"},
		{"c1 controls utf8", "a\u0080\u0085\u009ab", "ab"},
		{"c1 csi utf8 is a csi introducer", "a\u009b31mb", "ab"},
		{"raw c1 csi byte is a csi introducer", "a\x9b31mb", "ab"},
		{"raw c1 lone byte", "a\x85b", "ab"},
		{"c1 osc utf8 ended by bel", "a\u009d52;c;AAA\x07b", "ab"},
		{"c1 osc utf8 ended by c1 st", "a\u009d0;x\u009cb", "ab"},
		{"esc osc ended by utf8 c1 st", "a\x1b]0;x\u009cb", "ab"},
		{"esc osc ended by raw c1 st", "a\x1b]0;x\x9cb", "ab"},
		{"c1 dcs utf8 ended by esc st", "a\u0090q#0\x1b\\b", "ab"},
		{"raw c1 dcs ended by raw st", "a\x90q#0\x9cb", "ab"},
		{"c1 apc pm sos utf8", "a\u009fp\u009cb\u009ep\u009cc\u0098p\u009cd", "abcd"},
		{"unterminated c1 osc", "keep\u009d0;title", "keep"},
		{"utf8 continuation 0x9c inside osc does not end it", "a\x1b]0;东\x07b", "ab"},
		{"utf8 continuation bytes in c1 range kept", "世东\u4e16", "世东世"},
		{"invalid non-c1 byte kept", "a\xffb", "a\xffb"},
		{"mixed", "ok\x1b]52;c;AAA\x07 \x1b[2Jdone\x00\r\n", "ok done\n"},
		{"unterminated csi", "keep\x1b[12;", "keep"},
		{"unterminated osc", "keep\x1b]52;c;AAAA", "keep"},
		{"unterminated dcs", "keep\x1bPabc\x1b", "keep"},
		{"lone trailing esc", "keep\x1b", "keep"},
		{"esc swallows no text after terminated csi", "\x1b[Atext", "text"},
		{"two byte esc keeps utf8 intact", "a\x1b好b", "ab"},
		{"csi aborted by new esc", "a\x1b[12\x1b[2Jb", "ab"},
		{"osc aborted by non-st esc", "a\x1b]0;t\x1b[2Jb", "ab"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Strip(c.in); got != c.want {
				t.Fatalf("Strip(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
