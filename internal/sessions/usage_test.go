package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
)

func seedUsage(t *testing.T, d *db.DB, sess, source, model string, total int64) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO session_usage(session_uuid, source, model, input_tokens, total_tokens) VALUES (?,?,?,?,?)`,
		sess, source, model, total, total); err != nil {
		t.Fatal(err)
	}
}

func seedSessionWithSource(t *testing.T, d *db.DB, uuid, project, source, file string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title, source) VALUES (?,?,?,?,?,1,?,?)`,
		uuid, project, file, time.Now().Unix(), time.Now().Unix(), "title-"+uuid, source); err != nil {
		t.Fatal(err)
	}
}

func markStaleFile(t *testing.T, d *db.DB, file string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO ingest_state(source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, head_fingerprint, last_ingested_at, unparsed_lines, usage_stale)
		VALUES (?,1,1,1,'','',?,0,1)`, file, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
}

func TestUsageGroupedStalePropagation(t *testing.T) {
	d := testDB(t)
	seedSessionWithSource(t, d, "s1", "/p/a", "claude-code", "f1.jsonl")
	seedSessionWithSource(t, d, "s2", "/p/a", "claude-code", "f2.jsonl")
	seedSessionWithSource(t, d, "s3", "/p/b", "claude-code", "f3.jsonl")
	seedUsage(t, d, "s1", "claude-code", "m1", 100)
	seedUsage(t, d, "s2", "claude-code", "m1", 200)
	seedUsage(t, d, "s3", "claude-code", "m1", 50)
	markStaleFile(t, d, "f2.jsonl") // s2's usage is stale

	rows, err := UsageGrouped(context.Background(), d, "project", 0, "", "all", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2 projects", len(rows))
	}
	// /p/a first (300 tokens, stale sessions still INCLUDED in the total).
	if rows[0].Key != "/p/a" || rows[0].TotalTokens != 300 || rows[0].Sessions != 2 {
		t.Fatalf("rows[0]=%+v want /p/a 300 tokens 2 sessions", rows[0])
	}
	if rows[0].StaleSessions != 1 {
		t.Fatalf("stale_sessions=%d want 1 (stale propagates to the group)", rows[0].StaleSessions)
	}
	if rows[1].Key != "/p/b" || rows[1].StaleSessions != 0 {
		t.Fatalf("rows[1]=%+v want /p/b with no stale", rows[1])
	}

	// Source subtotal carries the same stale count.
	totals, err := UsageSourceTotals(context.Background(), d, 0, "", "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(totals) != 1 || totals[0].TotalTokens != 350 || totals[0].StaleSessions != 1 {
		t.Fatalf("totals=%+v want one source, 350 tokens, 1 stale session", totals)
	}
}

func TestUsageBySessionMarksStaleAndSumsModels(t *testing.T) {
	d := testDB(t)
	seedSessionWithSource(t, d, "s1", "/p", "claude-code", "f1.jsonl")
	seedUsage(t, d, "s1", "claude-code", "m1", 100)
	seedUsage(t, d, "s1", "claude-code", "m2", 11)
	markStaleFile(t, d, "f1.jsonl")

	rows, err := UsageBySession(context.Background(), d, 0, "", "all", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TotalTokens != 111 || !rows[0].Stale {
		t.Fatalf("rows=%+v want one row, 111 tokens, stale", rows)
	}
}

func TestUsageSourcesStaySeparate(t *testing.T) {
	d := testDB(t)
	seedSessionWithSource(t, d, "c1", "/p", "claude-code", "f1.jsonl")
	seedSessionWithSource(t, d, "x1", "/p", "codex", "f2.jsonl")
	seedUsage(t, d, "c1", "claude-code", "m1", 100)
	seedUsage(t, d, "x1", "codex", "(mixed)", 100)

	totals, err := UsageSourceTotals(context.Background(), d, 0, "", "all", "")
	if err != nil {
		t.Fatal(err)
	}
	// Two per-source subtotals; the query never emits a combined grand total.
	if len(totals) != 2 {
		t.Fatalf("totals=%+v want two per-source rows", totals)
	}
	for _, tot := range totals {
		if tot.TotalTokens != 100 {
			t.Fatalf("subtotal=%+v want 100 per source (never combined)", tot)
		}
	}
}

func TestSessionTotalTokensPlaceholderSemantics(t *testing.T) {
	d := testDB(t)
	seedSessionWithSource(t, d, "s1", "/p", "claude-code", "f1.jsonl")
	seedSessionWithSource(t, d, "s2", "/p", "claude-code", "f2.jsonl")
	seedUsage(t, d, "s1", "claude-code", "m1", 42)

	totals, stale, err := SessionTotalTokens(context.Background(), d, []string{"s1", "s2"})
	if err != nil {
		t.Fatal(err)
	}
	if totals["s1"] != 42 {
		t.Fatalf("totals=%v", totals)
	}
	if _, present := totals["s2"]; present {
		t.Fatal("s2 must be ABSENT (placeholder), not zero")
	}
	if len(stale) != 0 {
		t.Fatalf("stale=%v want empty", stale)
	}
}
