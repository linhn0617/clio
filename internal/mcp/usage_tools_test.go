package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
)

func addUsage(t *testing.T, d *db.DB, sess, source, model string, input, total int64) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO session_usage(session_uuid, source, model, input_tokens, total_tokens) VALUES (?,?,?,?,?)`,
		sess, source, model, input, total); err != nil {
		t.Fatal(err)
	}
}

func addQuotaSnapshot(t *testing.T, d *db.DB) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO quota_snapshots(source, limit_id, observed_at, used_percent, window_minutes, resets_at, plan_type, raw_json)
		VALUES ('codex','codex:primary',?,42.5,10080,?, 'plus','{"used_percent":42.5}')`,
		time.Now().Unix(), time.Now().Add(24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
}

func TestHandleActivitySummaryUsage(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/a")
	addSession(t, d, "s2", "/p/b")
	addUsage(t, d, "s1", "claude-code", "model-one", 100, 150)
	addUsage(t, d, "s1", "claude-code", "model-two", 10, 15)
	addUsage(t, d, "s2", "claude-code", "model-one", 1000, 1500)

	m := resultJSON(t, call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "usage", "source": "all"}))
	sess, ok := m["sessions"].([]any)
	if !ok || len(sess) != 2 {
		t.Fatalf("sessions=%v want 2 rows", m["sessions"])
	}
	// Ranked by total tokens within the source: s2 first.
	first := sess[0].(map[string]any)
	if first["session_uuid"] != "s2" || first["total_tokens"].(float64) != 1500 {
		t.Fatalf("first row=%v want s2/1500", first)
	}
	// s1 sums its per-model rows.
	second := sess[1].(map[string]any)
	if second["session_uuid"] != "s1" || second["total_tokens"].(float64) != 165 {
		t.Fatalf("second row=%v want s1/165", second)
	}
	if _, hasTotals := m["totals"]; !hasTotals {
		t.Fatal("missing per-source totals")
	}
}

// TestNoQuotaFieldEverCrossesMCP asserts the spec's privacy boundary: quota
// snapshot data (used_percent, resets_at, plan_type, credits, window identity)
// never appears in ANY MCP tool response — there is no flag to enable it.
func TestNoQuotaFieldEverCrossesMCP(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/a")
	addMsg(t, d, "s1", 0, "user", "hello usage world")
	addUsage(t, d, "s1", "codex", "(mixed)", 100, 150)
	addQuotaSnapshot(t, d)

	responses := map[string]string{}
	responses["activity_summary_usage"] = rawResult(t, call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "usage", "source": "all"}))
	responses["activity_summary_day"] = rawResult(t, call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "day", "source": "all"}))
	responses["list_sessions"] = rawResult(t, call(t, handleListSessions(d, nil), map[string]any{"source": "all"}))
	responses["search"] = rawResult(t, call(t, handleSearch(d, nil), map[string]any{"query": "usage", "source": "all"}))
	responses["read_session"] = rawResult(t, call(t, handleReadSession(d, nil), map[string]any{"session": "s1"}))
	responses["ask"] = rawResult(t, call(t, handleAsk(d, nil), map[string]any{"question": "what usage happened"}))

	for name, body := range responses {
		for _, forbidden := range []string{"used_percent", "resets_at", "plan_type", "credits", "window_minutes", "quota"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s response leaks quota field %q:\n%s", name, forbidden, body)
			}
		}
	}
}

func rawResult(t *testing.T, r any) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHandleActivitySummaryUsageStaleField(t *testing.T) {
	d := testDB(t)
	addSession(t, d, "s1", "/p/a")
	addUsage(t, d, "s1", "claude-code", "m1", 100, 150)
	// Mark s1's source file stale (last usage scan failed).
	if _, err := d.Exec(`INSERT INTO ingest_state(source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, head_fingerprint, last_ingested_at, unparsed_lines, usage_stale)
		VALUES ('s1.jsonl',1,1,1,'','',1,0,1)`); err != nil {
		t.Fatal(err)
	}
	m := resultJSON(t, call(t, handleActivitySummary(d, nil), map[string]any{"group_by": "usage", "source": "all"}))
	sess := m["sessions"].([]any)
	if len(sess) != 1 {
		t.Fatalf("sessions=%v", sess)
	}
	if stale, _ := sess[0].(map[string]any)["stale"].(bool); !stale {
		t.Fatalf("stale field missing/false on stale session: %v", sess[0])
	}
}
