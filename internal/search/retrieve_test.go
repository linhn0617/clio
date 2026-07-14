package search

import (
	"context"
	"testing"
	"time"
)

// TestRetrieveAnyTermMatchesEitherTerm is the core recall guarantee for `ask`:
// a message matching ANY query term is a candidate, even when no single message
// contains all terms — exactly the case Search (all-terms AND) misses.
func TestRetrieveAnyTermMatchesEitherTerm(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "assistant", "we fixed the authentication race", now)  // "authentication"
	addMsg(t, d, "s1", 1, "assistant", "the database migration was tricky", now) // "migration"

	cands, err := Retrieve(context.Background(), d, terms("authentication migration"), Options{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("OR retrieve expected 2 candidates, got %d: %+v", len(cands), cands)
	}

	// Sanity: the all-terms AND search finds neither message.
	res, err := Search(context.Background(), d, Options{Query: "authentication migration", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Fatalf("AND search should find no single message with both terms, got %d", len(res))
	}
}

// TestRetrieveIncludesShortTermsInMixedQuery guards the codex P2: a query mixing a
// long term with a short discriminator must still retrieve a message that matches
// only the short term — not collapse to the long-term-only candidate set.
func TestRetrieveIncludesShortTermsInMixedQuery(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "assistant", "the authentication layer is solid", now) // long "authentication"
	addMsg(t, d, "s1", 1, "assistant", "we use go for the worker", now)          // short "go" only

	cands, err := Retrieve(context.Background(), d, terms("authentication go"), Options{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var seqs []int
	for _, c := range cands {
		seqs = append(seqs, c.Seq)
	}
	if len(cands) != 2 {
		t.Fatalf("mixed long+short should retrieve both messages, got %d: seqs %v", len(cands), seqs)
	}
}

// TestRetrieveShortTermRanksByMatchCount guards the codex P2: short-term (LIKE)
// hits must be ranked by how many query terms they match, not purely by recency —
// an older message matching both terms outranks a newer one matching only one.
func TestRetrieveShortTermRanksByMatchCount(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	old := time.Now().Add(-100 * 24 * time.Hour).Unix()
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "ab cd here", old)   // matches both "ab" and "cd"
	addMsg(t, d, "s1", 1, "user", "ab only here", now) // matches "ab" only, but newer

	cands, err := Retrieve(context.Background(), d, terms("ab cd"), Options{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) < 2 || cands[0].Seq != 0 {
		t.Fatalf("two-match message should rank first despite being older, got %+v", cands)
	}
}

// TestRetrieveFTSTierRanksBeforeLikeOnly guards the codex round-3 P1: FTS (full
// term) hits and LIKE (substring) hits live on different score scales, so a
// LIKE-only hit must never be ranked above an FTS hit regardless of how many short
// terms it matches or how recent it is. The tier is explicit (Candidate.FTS).
func TestRetrieveFTSTierRanksBeforeLikeOnly(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "the authentication design notes", now-100000) // FTS: "authentication", older
	addMsg(t, d, "s1", 1, "user", "go ci ab here", now)                          // LIKE-only: go/ci/ab, newer, 3 matches

	cands, err := Retrieve(context.Background(), d, terms("authentication go ci ab"), Options{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) < 2 {
		t.Fatalf("want both hits, got %d: %+v", len(cands), cands)
	}
	seenLike := false
	for _, c := range cands {
		if !c.FTS {
			seenLike = true
		} else if seenLike {
			t.Fatalf("an FTS hit ranked after a LIKE-only hit: %+v", cands)
		}
	}
	if !cands[0].FTS {
		t.Fatalf("top candidate should be the FTS hit, got %+v", cands[0])
	}
}

// TestRetrieveCapsPerSession guards the codex P2: a per-session cap keeps one
// session that repeats the query terms across many turns from dominating (and
// starving) the candidate pool handed to grouping.
func TestRetrieveCapsPerSession(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "dominant", "/p")
	addSession(t, d, "other", "/p")
	now := time.Now().Unix()
	for i := range 20 {
		addMsg(t, d, "dominant", i, "user", "authentication detail", now-int64(i))
	}
	addMsg(t, d, "other", 0, "user", "authentication summary", now-1000)

	cands, err := Retrieve(context.Background(), d, terms("authentication"), Options{Limit: 50, MaxPerSession: 3})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, c := range cands {
		counts[c.SessionUUID]++
	}
	if counts["dominant"] > 3 {
		t.Fatalf("dominant session should be capped at 3, got %d", counts["dominant"])
	}
	if counts["other"] != 1 {
		t.Fatalf("other session must survive the cap, got %d", counts["other"])
	}
}

// TestRetrieveBothTierHitOutranksLongOnly guards the codex round-6 P2: in a mixed
// query, a message matching both a long (FTS) term and a short term should rank
// above one matching only the long term — the extra short match must not be lost
// when the both-tier hit is deduped to its FTS row.
func TestRetrieveBothTierHitOutranksLongOnly(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "authentication only here", now)   // long term only
	addMsg(t, d, "s1", 1, "user", "authentication and go here", now) // long + short "go"

	cands, err := Retrieve(context.Background(), d, terms("authentication go"), Options{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	pos := map[int]int{}
	for i, c := range cands {
		pos[c.Seq] = i
	}
	if pos[1] >= pos[0] {
		t.Fatalf("both-tier hit (seq1) should rank above long-only (seq0): %+v", cands)
	}
}

// TestRetrievePopulatesSeq verifies candidates carry the in-session seq used for
// windowing.
func TestRetrievePopulatesSeq(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 5, "assistant", "unique zzzmarker here", now)

	cands, err := Retrieve(context.Background(), d, terms("zzzmarker"), Options{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Seq != 5 {
		t.Fatalf("expected one candidate with seq=5, got %+v", cands)
	}
}
