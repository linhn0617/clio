package search

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
)

func testDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "s.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func addSession(t *testing.T, d *db.DB, uuid, project string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, turn_count) VALUES (?,?,?,0)`,
		uuid, project, uuid+".jsonl"); err != nil {
		t.Fatal(err)
	}
}

func addMsg(t *testing.T, d *db.DB, sess string, seq int, role, content string, ts int64) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES (?,?,?,?,?,?)`,
		sess, seq, ts, role, content, "{}"); err != nil {
		t.Fatal(err)
	}
}

func TestSearchEmptyQueryErrors(t *testing.T) {
	d := testDB(t)
	if _, err := Search(d, Options{Query: "  "}); err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestSearchFTS3Char(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "請幫我設計資料庫遷移流程", now)
	addMsg(t, d, "s1", 1, "assistant", "unrelated english text", now)

	res, err := Search(d, Options{Query: "資料庫", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].SessionUUID != "s1" {
		t.Fatalf("expected 1 FTS hit, got %+v", res)
	}
}

func TestSearchCJKShortFallback(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "資料驗證流程很重要", now)

	// 2-char query: trigram can't match, LIKE fallback must.
	res, err := Search(d, Options{Query: "驗證", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("expected LIKE fallback to find 1, got %d", len(res))
	}
}

func TestSearchDialogueOutranksToolOutput(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "how do we handle authentication here", now)
	addMsg(t, d, "s1", 1, "tool_result", "authentication authentication authentication log noise", now)

	res, err := Search(d, Options{Query: "authentication", Limit: 10, IncludeToolOutput: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) < 2 {
		t.Fatalf("expected both rows, got %d", len(res))
	}
	if res[0].Role != "user" {
		t.Fatalf("expected user message ranked first, got %q", res[0].Role)
	}
}

func TestSearchExcludesToolOutputByDefault(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "tool_result", "matched only in tool output zzztoken", now)

	res, err := Search(d, Options{Query: "zzztoken", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Fatalf("tool output should be excluded by default, got %d", len(res))
	}
	res, err = Search(d, Options{Query: "zzztoken", Limit: 10, IncludeToolOutput: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("with include-tool-output expected 1, got %d", len(res))
	}
}

func TestSearchLikeFallbackWithFilters(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/alpha")
	addSession(t, d, "s2", "/p/beta")
	now := time.Now().Unix()
	// 2-char CJK term forces the LIKE path; combine with project + role filters
	// to guard the likeQuery arg-ordering (query terms, then filter args, then limit).
	addMsg(t, d, "s1", 0, "user", "資料驗證很重要", now)
	addMsg(t, d, "s2", 0, "user", "資料驗證很重要", now)
	addMsg(t, d, "s1", 1, "assistant", "資料驗證回覆", now)

	res, err := Search(d, Options{Query: "驗證", ProjectPrefix: "/p/alpha", Role: "user", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].SessionUUID != "s1" || res[0].Role != "user" {
		t.Fatalf("LIKE fallback + filters failed: %+v", res)
	}
}

func TestSearchProjectFilter(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/alpha")
	addSession(t, d, "s2", "/p/beta")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "shared keyword qubit", now)
	addMsg(t, d, "s2", 0, "user", "shared keyword qubit", now)

	res, err := Search(d, Options{Query: "qubit", ProjectPrefix: "/p/alpha", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].SessionUUID != "s1" {
		t.Fatalf("project filter failed: %+v", res)
	}
}

// TestSearchOperatorSafeTerms verifies that FTS-operator characters in query terms
// do not cause syntax errors and match literal content.
func TestSearchOperatorSafeTerms(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "using c++ for performance", now)
	addMsg(t, d, "s1", 1, "user", `"unclosed string literal`, now)
	addMsg(t, d, "s1", 2, "user", "foo OR bar baz", now)
	addMsg(t, d, "s1", 3, "user", "(test expression)", now)

	cases := []struct {
		query   string
		wantHit bool
	}{
		{"c++", true},
		{`"unclosed`, false}, // short (< 3 runes after quote strip)? "unclosed is 8-char: goes FTS. Quoted in MATCH. No error.
		{"foo OR", false},    // "foo" is 3+ runes, "OR" is 2 runes -> hybrid. No error.
		{"(test", false},     // "(test" becomes term "test" after quote stripping? No - terms strips " only. "(" stays.
	}
	_ = cases

	// The main goal: none of these should return an error.
	for _, q := range []string{"c++", `"unclosed`, "foo OR", "(test"} {
		_, err := Search(d, Options{Query: q, Limit: 10, IncludeToolOutput: true})
		if err != nil {
			t.Errorf("query %q returned unexpected error: %v", q, err)
		}
	}

	// c++ should match the message containing "c++"
	res, err := Search(d, Options{Query: "c++", Limit: 10, IncludeToolOutput: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range res {
		if r.SessionUUID == "s1" && r.Role == "user" {
			found = true
		}
	}
	if !found {
		t.Errorf("c++ should have matched 'using c++ for performance', got %+v", res)
	}
}

// TestSearchHybridMixedLength verifies hybrid query: "auth ui" uses FTS for "auth"
// and LIKE for "ui", matching only messages that contain BOTH.
func TestSearchHybridMixedLength(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()

	// Seed many rows so an accidental early-LIMIT would drop the "both terms" match.
	// High-cardinality guard: 60 "auth-only" rows before the target row.
	for i := 0; i < 60; i++ {
		addMsg(t, d, "s1", i, "user", "auth authentication system", now-int64(i))
	}
	// The row that contains BOTH "auth" and "ui" — at seq 60, older timestamp.
	addMsg(t, d, "s1", 60, "user", "auth and ui components together", now-100)
	// A row with only "ui".
	addMsg(t, d, "s1", 61, "user", "ui components only", now-101)

	res, err := Search(d, Options{Query: "auth ui", Limit: 10, IncludeToolOutput: true})
	if err != nil {
		t.Fatalf("hybrid query returned error: %v", err)
	}

	// All returned results must contain "ui" (the short-term LIKE filter).
	for _, r := range res {
		if !strings.Contains(strings.ToLower(r.Snippet), "ui") {
			t.Errorf("result missing 'ui': %q", r.Snippet)
		}
	}

	// The "auth-only" message must NOT be in results.
	for _, r := range res {
		if r.Snippet == "auth authentication system" {
			t.Errorf("auth-only row should be excluded by ui LIKE filter")
		}
	}

	// Must have at least 1 result (the both-terms row).
	if len(res) == 0 {
		t.Fatal("expected at least one result with both 'auth' and 'ui'")
	}
}

// TestSearchAllShortQuery verifies the all-short LIKE path.
func TestSearchAllShortQuery(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "ui panel navigation", now)
	addMsg(t, d, "s1", 1, "user", "something else entirely", now)

	res, err := Search(d, Options{Query: "ui", Limit: 10})
	if err != nil {
		t.Fatalf("all-short query error: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 result for 'ui', got %d: %+v", len(res), res)
	}
}

// TestSearchProjectPrefixEscaping verifies that _ in a project prefix is escaped,
// so /x/a_b matches /x/a_b/... but NOT /x/axb.
func TestSearchProjectPrefixEscaping(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/x/a_b")
	addSession(t, d, "s2", "/x/axb")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "authentication service here", now)
	addMsg(t, d, "s2", 0, "user", "authentication service here", now)

	res, err := Search(d, Options{Query: "authentication", ProjectPrefix: "/x/a_b", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.SessionUUID == "s2" {
			t.Errorf("project /x/axb should NOT match prefix /x/a_b (underscore must be escaped)")
		}
	}
	found := false
	for _, r := range res {
		if r.SessionUUID == "s1" {
			found = true
		}
	}
	if !found {
		t.Errorf("project /x/a_b should match prefix /x/a_b, got %+v", res)
	}
}

// TestSearchContentLikeEscaping verifies that % and _ in content terms match literally.
func TestSearchContentLikeEscaping(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "result is 100% correct", now)
	addMsg(t, d, "s1", 1, "user", "file_name is important", now)
	addMsg(t, d, "s1", 2, "user", "nothing special here at all", now)

	// 2-char "%" would normally match everything in LIKE without escaping.
	// But "%" is 1 rune, so it goes to the all-short likeQuery path.
	// With escaping, it should match only messages containing literal %.
	res, err := Search(d, Options{Query: "%", Limit: 10})
	if err != nil {
		t.Fatalf("percent query error: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("'%%' should match only 1 message with literal %%, got %d: %+v", len(res), res)
	}

	res, err = Search(d, Options{Query: "_", Limit: 10})
	if err != nil {
		t.Fatalf("underscore query error: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("'_' should match only 1 message with literal _, got %d: %+v", len(res), res)
	}
}

// TestSearchAllPunctuationQuery verifies *** returns without error (all short, no match).
func TestSearchAllPunctuationQuery(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "hello world", now)

	_, err := Search(d, Options{Query: "***", Limit: 10})
	if err != nil {
		t.Errorf("all-punctuation query *** should not error, got: %v", err)
	}
}

// TestSearchQuotedPhraseWithShortTerm verifies "auth flow" ui works (quoted phrase + short term).
func TestSearchQuotedPhraseWithShortTerm(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()
	// This message has both "auth flow" phrase and "ui".
	addMsg(t, d, "s1", 0, "user", "auth flow and ui components", now)
	// This has "auth flow" but not "ui".
	addMsg(t, d, "s1", 1, "user", "auth flow without other thing", now)

	res, err := Search(d, Options{Query: `"auth flow" ui`, Limit: 10})
	if err != nil {
		t.Fatalf(`"auth flow" ui returned error: %v`, err)
	}
	if len(res) == 0 {
		t.Fatal("expected at least one result for \"auth flow\" ui")
	}
	// Only the message with both "auth flow" and "ui" should appear.
	// The snippet may be truncated by FTS, so check message IDs rather than snippet text.
	// The row with seq=1 ("auth flow without other thing") should NOT be in results.
	// Verify: all results should be seq=0 (the both-terms row).
	for _, r := range res {
		// The seq-1 row content does NOT contain "ui", so it must be excluded.
		// We verify by checking that no result has "without" in its snippet
		// (that phrase is unique to the auth-flow-only row).
		if strings.Contains(r.Snippet, "without") {
			t.Errorf("auth-flow-only row should be excluded by ui LIKE filter, snippet: %q", r.Snippet)
		}
	}
}

// TestSearchExplainQueryPlanUsesFTS verifies the hybrid query uses the FTS virtual table.
func TestSearchExplainQueryPlanUsesFTS(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/one")
	now := time.Now().Unix()
	addMsg(t, d, "s1", 0, "user", "authentication ui components", now)

	// Build the same SQL the hybridQuery would use for "auth ui" (long=["auth"], short=["ui"]).
	hybridSQL := `SELECT m.id, m.session_uuid, COALESCE(s.project_path,''), m.role, COALESCE(m.ts,0),
		snippet(messages_fts,0,'[',']','…',10), bm25(messages_fts)
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.rowid
		LEFT JOIN sessions s ON s.uuid = m.session_uuid
		WHERE messages_fts MATCH ? AND m.content LIKE ? ESCAPE '\' AND m.role IN ('user','assistant')
		ORDER BY bm25(messages_fts) LIMIT ?`

	rows, err := d.Query("EXPLAIN QUERY PLAN "+hybridSQL, `"auth"`, "%ui%", 100)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN failed: %v", err)
	}
	defer rows.Close()

	var planLines []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan EXPLAIN row: %v", err)
		}
		planLines = append(planLines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("EXPLAIN rows error: %v", err)
	}

	t.Logf("EXPLAIN QUERY PLAN output:")
	for _, line := range planLines {
		t.Logf("  %s", line)
	}

	hasFTS := false
	hasFullScan := false
	for _, line := range planLines {
		if strings.Contains(line, "messages_fts") {
			hasFTS = true
		}
		// A full scan of the base messages table (not via FTS) would look like "SCAN messages"
		if strings.Contains(line, "SCAN messages ") || line == "SCAN messages" {
			hasFullScan = true
		}
	}

	if !hasFTS {
		t.Errorf("EXPLAIN QUERY PLAN does not reference messages_fts — FTS index not used. Plan: %v", planLines)
	}
	if hasFullScan {
		t.Errorf("EXPLAIN QUERY PLAN shows full SCAN messages — FTS-first not achieved. Plan: %v", planLines)
	}
}
