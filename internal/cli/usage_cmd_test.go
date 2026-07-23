package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
)

// usageCmdSandbox points HOME/XDG at temp dirs, creates the index DB there,
// and seeds it. Commands then run the real openAndCatchUp path without ever
// touching the developer's actual index.
func usageCmdSandbox(t *testing.T) *db.DB {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	dbPath, err := config.DBPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func seedUsageSession(t *testing.T, d *db.DB, uuid, project, source, model string, total int64, stale bool) {
	t.Helper()
	file := uuid + ".jsonl"
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title, source) VALUES (?,?,?,?,?,1,?,?)`,
		uuid, project, file, time.Now().Unix(), time.Now().Unix(), "title-"+uuid, source); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO session_usage(session_uuid, source, model, input_tokens, total_tokens) VALUES (?,?,?,?,?)`,
		uuid, source, model, total, total); err != nil {
		t.Fatal(err)
	}
	if stale {
		if _, err := d.Exec(`INSERT INTO ingest_state(source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, head_fingerprint, last_ingested_at, unparsed_lines, usage_stale) VALUES (?,1,1,1,'','',1,0,1)`, file); err != nil {
			t.Fatal(err)
		}
	}
}

// runCmd executes a cobra command capturing os.Stdout (the command writes to
// os.Stdout directly).
func runCmd(t *testing.T, args ...string) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	cmd := newUsageCmd()
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	execErr := cmd.Execute()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	if execErr != nil {
		t.Fatalf("command failed: %v\noutput:\n%s", execErr, buf.String())
	}
	return buf.String()
}

func TestUsageCmdSourceSeparationNoGrandTotal(t *testing.T) {
	d := usageCmdSandbox(t)
	seedUsageSession(t, d, "c1", "/p", "claude-code", "m1", 100, false)
	seedUsageSession(t, d, "x1", "/p", "codex", "(mixed)", 100, false)
	out := runCmd(t, "--source", "all")
	if !strings.Contains(out, "claude-code — 100 tokens across 1 sessions") ||
		!strings.Contains(out, "codex — 100 tokens across 1 sessions") {
		t.Fatalf("missing per-source headers:\n%s", out)
	}
	if strings.Contains(out, "200") {
		t.Fatalf("cross-source grand total leaked:\n%s", out)
	}
}

func TestUsageCmdEmptyStatePointsAtFullReindex(t *testing.T) {
	usageCmdSandbox(t)
	out := runCmd(t, "--source", "all")
	if !strings.Contains(out, "clio index --full") {
		t.Fatalf("empty state must point at the backfill command:\n%s", out)
	}
}

func TestUsageCmdModelFilterAppliesToSubtotals(t *testing.T) {
	d := usageCmdSandbox(t)
	seedUsageSession(t, d, "s1", "/p", "claude-code", "model-a", 100, false)
	seedUsageSession(t, d, "s2", "/p", "claude-code", "model-b", 900, false)
	out := runCmd(t, "--source", "all", "--model", "model-a")
	if !strings.Contains(out, "100 tokens across 1 sessions") {
		t.Fatalf("subtotal must honor the model filter:\n%s", out)
	}
	if strings.Contains(out, "1.0k tokens") || strings.Contains(out, "s2") {
		t.Fatalf("unfiltered subtotal/session leaked:\n%s", out)
	}
	// Nonexistent model: filtered empty state, not a nonzero header.
	out2 := runCmd(t, "--source", "all", "--model", "nope")
	if !strings.Contains(out2, "no usage data matches these filters") {
		t.Fatalf("filtered empty state missing:\n%s", out2)
	}
}

func TestUsageCmdStaleMarkersRendered(t *testing.T) {
	d := usageCmdSandbox(t)
	seedUsageSession(t, d, "s1", "/p", "claude-code", "m1", 100, true)
	out := runCmd(t, "--source", "all")
	if !strings.Contains(out, "[stale: 1 sessions]") || !strings.Contains(out, "[stale]") {
		t.Fatalf("stale markers missing on subtotal/session row:\n%s", out)
	}
}

func TestUsageCmdQuotaDisclaimerAndAge(t *testing.T) {
	d := usageCmdSandbox(t)
	observed := time.Now().Add(-3 * time.Hour).Unix()
	if _, err := d.Exec(`INSERT INTO quota_snapshots(source, limit_id, observed_at, used_percent, window_minutes, resets_at, plan_type) VALUES ('codex','codex:primary',?,42.5,10080,?, 'plus')`,
		observed, time.Now().Add(72*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	out := runCmd(t, "--quota")
	for _, want := range []string{"NOT live readings", "42.5% used", "observed 3h ago", "resets in 2d"} {
		if !strings.Contains(out, want) {
			t.Fatalf("quota output missing %q:\n%s", want, out)
		}
	}
	// A snapshot past its reset time renders STALE.
	if _, err := d.Exec(`UPDATE quota_snapshots SET resets_at = ?`, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if out := runCmd(t, "--quota"); !strings.Contains(out, "STALE") {
		t.Fatalf("past-reset snapshot must render STALE:\n%s", out)
	}
}

func TestListJSONIncludesUsage(t *testing.T) {
	d := usageCmdSandbox(t)
	seedUsageSession(t, d, "s1", "/p", "claude-code", "m1", 1500, true)
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title, source) VALUES ('s2','/p','s2.jsonl',1,2,1,'no usage','claude-code')`); err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	cmd := newListCmd()
	cmd.SetArgs([]string{"--json", "--source", "all"})
	cmd.SilenceUsage = true
	execErr := cmd.Execute()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	if execErr != nil {
		t.Fatalf("list --json failed: %v", execErr)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("bad json: %v\n%s", err, buf.String())
	}
	byUUID := map[string]map[string]any{}
	for _, r := range rows {
		byUUID[r["UUID"].(string)] = r
	}
	if tt, ok := byUUID["s1"]["total_tokens"].(float64); !ok || tt != 1500 {
		t.Fatalf("s1 total_tokens missing/wrong: %v", byUUID["s1"])
	}
	if st, _ := byUUID["s1"]["usage_stale"].(bool); !st {
		t.Fatalf("s1 usage_stale missing: %v", byUUID["s1"])
	}
	if _, present := byUUID["s2"]["total_tokens"]; present {
		t.Fatalf("s2 must omit total_tokens (no usage data): %v", byUUID["s2"])
	}
}
