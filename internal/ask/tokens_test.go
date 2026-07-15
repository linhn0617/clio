package ask

import "testing"

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii-only", "hello", 2},       // 5 runes -> ceil(5/4) = 2
		{"whitespace-only", "    ", 1},   // 4 runes -> ceil(4/4) = 1
		{"cjk-only", "資料庫", 3},           // 3 CJK runes -> 3 tokens
		{"mixed", "hello資料", 4},          // 5 ascii (ceil(5/4)=2) + 2 cjk = 4
		{"one-ascii-rune", "a", 1},       // ceil(1/4) = 1
		{"four-ascii-runes", "abcd", 1},  // ceil(4/4) = 1
		{"five-ascii-runes", "abcde", 2}, // ceil(5/4) = 2
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EstimateTokens(c.in); got != c.want {
				t.Fatalf("EstimateTokens(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// EstimateTokens must be the single source of truth for both enforcing the
// budget in Ask and (in the future retrieval regression suite) asserting it —
// so it must be deterministic across repeated calls on the same input.
func TestEstimateTokensDeterministic(t *testing.T) {
	s := "資料庫遷移 database migration plan 資料庫遷移"
	a := EstimateTokens(s)
	b := EstimateTokens(s)
	if a != b {
		t.Fatalf("EstimateTokens not deterministic: %d vs %d", a, b)
	}
}

func TestEstimateGroupTokensSumsExcerpts(t *testing.T) {
	eg := EvidenceGroup{Excerpts: []Excerpt{
		{Text: "hello"}, // 2
		{Text: "資料庫"},   // 3
	}}
	if got, want := estimateGroupTokens(eg), 5; got != want {
		t.Fatalf("estimateGroupTokens = %d, want %d", got, want)
	}
}

func TestFloorTopGroupHitsKeepsOnlyHitsTruncatedToFloor(t *testing.T) {
	long := ""
	for i := 0; i < minExcerptRunes+20; i++ {
		long += "資"
	}
	eg := EvidenceGroup{
		SessionUUID: "s1",
		Title:       "t",
		Excerpts: []Excerpt{
			{Seq: 0, Text: long, IsHit: true},
			{Seq: 1, Text: "not a hit", IsHit: false},
			{Seq: 2, Text: "short hit", IsHit: true},
		},
	}
	got := floorTopGroupHits(eg)
	if got.SessionUUID != "s1" || got.Title != "t" {
		t.Fatalf("citation metadata not preserved: %+v", got)
	}
	if len(got.Excerpts) != 2 {
		t.Fatalf("want only the 2 hit excerpts, got %d: %+v", len(got.Excerpts), got.Excerpts)
	}
	for _, e := range got.Excerpts {
		if !e.IsHit {
			t.Fatalf("floor form must only contain hits: %+v", e)
		}
	}
	// The long hit must be cut to the floor; the already-short hit is untouched.
	if n := len([]rune(got.Excerpts[0].Text)); n > minExcerptRunes+1 { // +1 for the ellipsis rune
		t.Fatalf("long hit not truncated to the floor: %d runes (%q)", n, got.Excerpts[0].Text)
	}
	if got.Excerpts[1].Text != "short hit" {
		t.Fatalf("short hit should be unchanged: got %q", got.Excerpts[1].Text)
	}
}
