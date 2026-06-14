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

	cands, err := Retrieve(context.Background(), d, Options{Query: "authentication migration", Limit: 10})
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

	cands, err := Retrieve(context.Background(), d, Options{Query: "authentication go", Limit: 10})
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

	cands, err := Retrieve(context.Background(), d, Options{Query: "ab cd", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) < 2 || cands[0].Seq != 0 {
		t.Fatalf("two-match message should rank first despite being older, got %+v", cands)
	}
}

// TestRetrievePopulatesSeq verifies candidates carry the in-session seq used for
// windowing.
func TestRetrievePopulatesSeq(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 5, "assistant", "unique zzzmarker here", now)

	cands, err := Retrieve(context.Background(), d, Options{Query: "zzzmarker", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Seq != 5 {
		t.Fatalf("expected one candidate with seq=5, got %+v", cands)
	}
}
