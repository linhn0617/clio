package ask

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
)

// The bundle is a public JSON contract (clio ask --json); keys are snake_case.
func TestAnswerJSONUsesSnakeCase(t *testing.T) {
	a := Answer{
		Question: "q",
		Groups:   []EvidenceGroup{{SessionUUID: "s", Excerpts: []Excerpt{{IsHit: true}}}},
	}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, key := range []string{`"question"`, `"groups"`, `"session_uuid"`, `"excerpts"`, `"is_hit"`} {
		if !strings.Contains(out, key) {
			t.Fatalf("missing key %s in %s", key, out)
		}
	}
}

func testDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "a.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func addSession(t *testing.T, d *db.DB, uuid, project, title string) {
	t.Helper()
	now := time.Now().Unix()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title) VALUES (?,?,?,?,?,?,?)`,
		uuid, project, uuid+".jsonl", now, now, 0, title); err != nil {
		t.Fatal(err)
	}
}

func addMsg(t *testing.T, d *db.DB, sess string, seq int, role, content string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES (?,?,?,?,?,?)`,
		sess, seq, time.Now().Unix(), role, content, "{}"); err != nil {
		t.Fatal(err)
	}
}

// Session ranking rewards combined hit strength, not just the single best line:
// three solid hits out-rank one slightly-stronger hit, but extra hits beyond K
// don't inflate the score.
func TestTopKSumRewardsMultipleHits(t *testing.T) {
	if multi, single := topKSum([]float64{1, 1, 1}, 3), topKSum([]float64{2}, 3); !(multi > single) {
		t.Fatalf("multi-hit session (%v) should out-rank single-hit (%v)", multi, single)
	}
	if got := topKSum([]float64{5, 1, 1, 1, 1}, 2); got != 6 {
		t.Fatalf("top-2 of [5,1,1,1,1] = %v, want 6", got)
	}
}

func TestAskGroupsWindowsAndCites(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/proj", "Auth bug fix")
	addMsg(t, d, "s1", 0, "user", "we have an authentication problem")
	addMsg(t, d, "s1", 1, "assistant", "the fix was to refresh the token")
	addMsg(t, d, "s1", 2, "user", "thanks")

	ans, err := Ask(context.Background(), d, Options{Question: "how did we fix authentication?", Window: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Groups) != 1 {
		t.Fatalf("want 1 group, got %d: %+v", len(ans.Groups), ans.Groups)
	}
	g := ans.Groups[0]
	if g.SessionUUID != "s1" || g.Title != "Auth bug fix" || g.Project != "/proj" {
		t.Fatalf("citation fields missing/wrong: %+v", g)
	}
	hit := false
	for _, e := range g.Excerpts {
		if e.IsHit {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("no excerpt marked as a hit: %+v", g.Excerpts)
	}
	// The hit (seq 0) plus its windowed neighbor (seq 1) should both be present.
	if len(g.Excerpts) < 2 {
		t.Fatalf("expected windowed neighbors, got %d excerpts: %+v", len(g.Excerpts), g.Excerpts)
	}
}

// A session with a full-term (FTS) hit ranks above a session that only matches
// short substring (LIKE) terms, no matter how many short terms the latter piles
// up — the two score scales must not be summed against each other.
func TestAskRanksFTSSessionAboveLikeOnly(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "ftsess", "/p", "fts session")
	addSession(t, d, "likeonly", "/p", "like only")
	addMsg(t, d, "ftsess", 0, "user", "the authentication module overview")
	// Pile up short-term LIKE matches so an unscaled sum would win.
	addMsg(t, d, "likeonly", 0, "user", "go ci ab pipeline")
	addMsg(t, d, "likeonly", 1, "user", "go ci ab again")
	addMsg(t, d, "likeonly", 2, "user", "go ci ab more")

	ans, err := Ask(context.Background(), d, Options{Question: "authentication go ci ab"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Groups) < 2 || ans.Groups[0].SessionUUID != "ftsess" {
		t.Fatalf("FTS session should rank first; got %+v", ans.Groups)
	}
}

// Empty results must serialize groups as [] (a stable array), not null, matching
// the MCP tool's empty response.
func TestAnswerJSONEmptyGroupsIsArray(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", "t")
	addMsg(t, d, "s1", 0, "user", "completely unrelated content")

	ans, err := Ask(context.Background(), d, Options{Question: "nonexistent zzqq term"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(ans)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"groups":[]`) {
		t.Fatalf("empty groups must marshal as [], got %s", b)
	}
}

func TestAskNoMatchIsEmpty(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p", "t")
	addMsg(t, d, "s1", 0, "user", "completely unrelated text")

	ans, err := Ask(context.Background(), d, Options{Question: "quantum chromodynamics lagrangian"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Groups) != 0 {
		t.Fatalf("expected empty answer, got %+v", ans.Groups)
	}
}

func TestAskRanksStrongerSessionFirst(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "weak", "/p", "weak")
	addSession(t, d, "strong", "/p", "strong")
	// "strong" matches both query terms; "weak" matches one.
	addMsg(t, d, "weak", 0, "user", "something about migration only")
	addMsg(t, d, "strong", 0, "user", "the database migration plan in detail")

	ans, err := Ask(context.Background(), d, Options{Question: "database migration"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Groups) < 2 {
		t.Fatalf("want both sessions, got %d", len(ans.Groups))
	}
	if ans.Groups[0].SessionUUID != "strong" {
		t.Fatalf("stronger session should rank first, got %q", ans.Groups[0].SessionUUID)
	}
}
