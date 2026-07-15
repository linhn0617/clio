package ask

import (
	"context"
	"strings"
	"testing"
)

// A generous budget must reproduce the pre-budget bundle: same groups, same
// excerpt counts, unaffected by MaxTokens.
func TestAskBudgetGenerousBudgetPreservesFullBundle(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/proj", "Auth bug fix")
	addMsg(t, d, "s1", 0, "user", "we have an authentication problem")
	addMsg(t, d, "s1", 1, "assistant", "the fix was to refresh the token")
	addMsg(t, d, "s1", 2, "user", "thanks")

	def, err := Ask(context.Background(), d, Options{Question: "how did we fix authentication?", Window: 1})
	if err != nil {
		t.Fatal(err)
	}
	generous, err := Ask(context.Background(), d, Options{Question: "how did we fix authentication?", Window: 1, MaxTokens: maxMaxTokens})
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Groups) != len(generous.Groups) {
		t.Fatalf("default budget (%d groups) should already match a generous one (%d groups) for small test content",
			len(def.Groups), len(generous.Groups))
	}
	for i := range def.Groups {
		if got, want := len(def.Groups[i].Excerpts), len(generous.Groups[i].Excerpts); got != want {
			t.Fatalf("group %d excerpt count differs between default and generous budgets: %d vs %d", i, got, want)
		}
	}
}

// When the budget is too small to hold every group, the lowest-ranked whole
// groups are dropped first (rank order: uuid tiebreak here, since all three
// sessions are tied), and a kept group is never partially emitted — its
// excerpt count matches the unbudgeted assembly exactly.
func TestAskBudgetDropsLowestRankedWholeGroups(t *testing.T) {
	d := testDB(t)
	filler := strings.Repeat("the database migration plan covers schema changes and rollout steps ", 4)
	for _, uuid := range []string{"s1", "s2", "s3"} {
		addSession(t, d, uuid, "/p", uuid)
		addMsg(t, d, uuid, 0, "user", "database migration "+filler)
	}

	full, err := Ask(context.Background(), d, Options{Question: "database migration", MaxTokens: maxMaxTokens})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Groups) != 3 {
		t.Fatalf("setup: want all 3 tied sessions at a generous budget, got %+v", full.Groups)
	}
	refExcerpts := map[string]int{}
	for _, g := range full.Groups {
		refExcerpts[g.SessionUUID] = len(g.Excerpts)
	}
	perGroup := estimateGroupTokens(full.Groups[0])
	if perGroup == 0 {
		t.Fatal("setup: expected non-zero token content per group")
	}

	// A budget sized to fit exactly two of the three equal-sized groups.
	budget := perGroup*2 + perGroup/2
	small, err := Ask(context.Background(), d, Options{Question: "database migration", MaxTokens: budget})
	if err != nil {
		t.Fatal(err)
	}
	if len(small.Groups) != 2 {
		t.Fatalf("want exactly 2 groups kept for budget %d (per-group %d), got %d: %+v",
			budget, perGroup, len(small.Groups), small.Groups)
	}
	total := 0
	for _, g := range small.Groups {
		if got, want := len(g.Excerpts), refExcerpts[g.SessionUUID]; got != want {
			t.Fatalf("group %s partially emitted: %d excerpts, want %d (whole-group)", g.SessionUUID, got, want)
		}
		total += estimateGroupTokens(g)
	}
	if total > budget {
		t.Fatalf("bundle tokens %d exceed budget %d — large-history bound violated", total, budget)
	}
	got := []string{small.Groups[0].SessionUUID, small.Groups[1].SessionUUID}
	if got[0] != "s1" || got[1] != "s2" {
		t.Fatalf("want the lowest-ranked group (s3) dropped first, kept [s1 s2], got %v", got)
	}
}

// The keep-top-hits invariant takes precedence over the budget: when even the
// top-ranked group's hit excerpts at the floor exceed a very small budget,
// they are still returned (bundle exceeds the raw budget by design) rather
// than an empty bundle.
func TestAskBudgetTopGroupHitsSurviveTinyBudget(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "big", "/p", "big")
	filler := strings.Repeat("填充內容測試文字說明範例持續", 6) // 84 CJK runes, no query term
	content := "資料庫遷移" + filler                   // 89 CJK runes; all 3 msgs match the query
	for i := 0; i < 3; i++ {
		addMsg(t, d, "big", i, "user", content)
	}

	full, err := Ask(context.Background(), d, Options{Question: "資料庫遷移", MaxTokens: maxMaxTokens})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Groups) != 1 || len(full.Groups[0].Excerpts) != 3 {
		t.Fatalf("setup: want 1 group of 3 hit excerpts at a generous budget, got %+v", full.Groups)
	}
	wantFloor := floorTopGroupHits(full.Groups[0])
	wantTokens := estimateGroupTokens(wantFloor)
	if wantTokens <= minMaxTokens {
		t.Fatalf("setup: floor-truncated top group (%d tokens) must exceed minMaxTokens (%d) to exercise the invariant",
			wantTokens, minMaxTokens)
	}

	tiny, err := Ask(context.Background(), d, Options{Question: "資料庫遷移", MaxTokens: minMaxTokens})
	if err != nil {
		t.Fatal(err)
	}
	if len(tiny.Groups) != 1 {
		t.Fatalf("keep-top-hits invariant: want exactly 1 group even at a tiny budget, got %d: %+v", len(tiny.Groups), tiny.Groups)
	}
	g := tiny.Groups[0]
	if g.SessionUUID != "big" {
		t.Fatalf("want the top-ranked session's hits, got %q", g.SessionUUID)
	}
	if len(g.Excerpts) != 3 {
		t.Fatalf("want all 3 hit excerpts preserved (floor form), got %d: %+v", len(g.Excerpts), g.Excerpts)
	}
	for _, e := range g.Excerpts {
		if !e.IsHit {
			t.Fatalf("floor form must only contain hit excerpts, got a non-hit: %+v", e)
		}
	}
	got := estimateGroupTokens(g)
	if got != wantTokens {
		t.Fatalf("floor-truncated bundle tokens = %d, want %d (matching floorTopGroupHits of the full group)", got, wantTokens)
	}
	if got <= minMaxTokens {
		t.Fatalf("effective bound should exceed the raw budget here (by design): got %d tokens for a %d budget", got, minMaxTokens)
	}
}

// A negative or zero MaxTokens defaults, exactly like the other Options
// defaults (MaxSessions, Window, MaxExcerptLen).
func TestAskBudgetDefaultsWhenUnset(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", "t")
	addMsg(t, d, "s1", 0, "user", "authentication overview")

	unset, err := Ask(context.Background(), d, Options{Question: "authentication"})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := Ask(context.Background(), d, Options{Question: "authentication", MaxTokens: defaultMaxTokens})
	if err != nil {
		t.Fatal(err)
	}
	if len(unset.Groups) != len(explicit.Groups) {
		t.Fatalf("MaxTokens<=0 should default to %d: got %d groups vs %d", defaultMaxTokens, len(unset.Groups), len(explicit.Groups))
	}
}

// Caller-supplied budgets are clamped inside Ask itself, so every surface
// (CLI, MCP, direct callers) shares the same floor and ceiling.
func TestAskBudgetClampsCallerValues(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/proj", "Auth bug fix")
	addMsg(t, d, "s1", 0, "user", "we have an authentication problem")
	addMsg(t, d, "s1", 1, "assistant", "the fix was to refresh the token")

	// Below the floor behaves exactly like the floor.
	atFloor, err := Ask(context.Background(), d, Options{Question: "how did we fix authentication?", Window: 1, MaxTokens: minMaxTokens})
	if err != nil {
		t.Fatal(err)
	}
	below, err := Ask(context.Background(), d, Options{Question: "how did we fix authentication?", Window: 1, MaxTokens: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(below.Groups) != len(atFloor.Groups) {
		t.Fatalf("MaxTokens below the floor should behave like the floor: %d vs %d groups", len(below.Groups), len(atFloor.Groups))
	}
	for i := range below.Groups {
		if got, want := len(below.Groups[i].Excerpts), len(atFloor.Groups[i].Excerpts); got != want {
			t.Fatalf("group %d excerpt count differs between below-floor and at-floor budgets: %d vs %d", i, got, want)
		}
	}

	// Above the ceiling behaves exactly like the ceiling.
	atMax, err := Ask(context.Background(), d, Options{Question: "how did we fix authentication?", Window: 1, MaxTokens: maxMaxTokens})
	if err != nil {
		t.Fatal(err)
	}
	above, err := Ask(context.Background(), d, Options{Question: "how did we fix authentication?", Window: 1, MaxTokens: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(above.Groups) != len(atMax.Groups) {
		t.Fatalf("MaxTokens above the ceiling should behave like the ceiling: %d vs %d groups", len(above.Groups), len(atMax.Groups))
	}
}
