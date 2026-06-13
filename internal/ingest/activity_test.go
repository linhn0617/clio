package ingest

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/model"
)

func TestExtractTargets(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input string
		want  []model.ToolTarget
	}{
		{"edit", "Edit", `{"file_path":"/x/auth.ts","old_string":"a","new_string":"b"}`,
			[]model.ToolTarget{{Kind: "tool", Value: "Edit"}, {Kind: "file", Value: "/x/auth.ts"}}},
		{"write", "Write", `{"file_path":"/x/a.go","content":"..."}`,
			[]model.ToolTarget{{Kind: "tool", Value: "Write"}, {Kind: "file", Value: "/x/a.go"}}},
		{"read", "Read", `{"file_path":"/x/b.go"}`,
			[]model.ToolTarget{{Kind: "tool", Value: "Read"}, {Kind: "file", Value: "/x/b.go"}}},
		{"notebookedit", "NotebookEdit", `{"notebook_path":"/x/n.ipynb"}`,
			[]model.ToolTarget{{Kind: "tool", Value: "NotebookEdit"}, {Kind: "file", Value: "/x/n.ipynb"}}},
		{"multiedit", "MultiEdit", `{"file_path":"/x/c.go","edits":[{"old_string":"a","new_string":"b"}]}`,
			[]model.ToolTarget{{Kind: "tool", Value: "MultiEdit"}, {Kind: "file", Value: "/x/c.go"}}},
		{"bash", "Bash", `{"command":"go test ./..."}`,
			[]model.ToolTarget{{Kind: "tool", Value: "Bash"}, {Kind: "command", Value: "go test ./..."}}},
		{"grep", "Grep", `{"pattern":"func main"}`,
			[]model.ToolTarget{{Kind: "tool", Value: "Grep"}, {Kind: "pattern", Value: "func main"}}},
		{"glob", "Glob", `{"pattern":"**/*.go"}`,
			[]model.ToolTarget{{Kind: "tool", Value: "Glob"}, {Kind: "pattern", Value: "**/*.go"}}},
		{"webfetch", "WebFetch", `{"url":"https://example.com/x"}`,
			[]model.ToolTarget{{Kind: "tool", Value: "WebFetch"}, {Kind: "url", Value: "https://example.com/x"}}},
		{"unknown tool → tool only", "SomeTool", `{"foo":"bar"}`,
			[]model.ToolTarget{{Kind: "tool", Value: "SomeTool"}}},
		{"clio tool excluded", "mcp__clio__search", `{"query":"x"}`, nil},
		{"other mcp tool → tool only", "mcp__github__create_issue", `{"title":"x"}`,
			[]model.ToolTarget{{Kind: "tool", Value: "mcp__github__create_issue"}}},
		{"missing field → tool only", "Bash", `{"description":"no command here"}`,
			[]model.ToolTarget{{Kind: "tool", Value: "Bash"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTargets(tt.tool, json.RawMessage(tt.input))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("extractTargets(%q,%s)\n got = %#v\nwant = %#v", tt.tool, tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractTargetsCapsValueOnUTF8Boundary(t *testing.T) {
	long := "echo " + strings.Repeat("世", 400) // ~1205 bytes, well over the cap
	input, _ := json.Marshal(map[string]string{"command": long})
	var cmd string
	for _, g := range extractTargets("Bash", input) {
		if g.Kind == model.TargetCommand {
			cmd = g.Value
		}
	}
	if cmd == "" {
		t.Fatal("expected a command target")
	}
	if len(cmd) > maxTargetValueBytes {
		t.Errorf("command value %d bytes, want <= %d", len(cmd), maxTargetValueBytes)
	}
	if !utf8.ValidString(cmd) {
		t.Error("capped value is not valid UTF-8")
	}
}

// Regression guard for the `% len(blocks)` alignment (codex P2): two byte-identical
// adjacent events, each with two distinct tool_use blocks, must each get correctly
// aligned facts on backfill — none dropped. Without the modulo, the 3rd/4th messages
// would be skipped.
func TestBackfillAlignsDuplicateAdjacentEvents(t *testing.T) {
	projects := t.TempDir()
	ev := `{"type":"assistant","timestamp":"2026-04-26T11:00:05Z","sessionId":"dup",` +
		`"message":{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"x","name":"Read","input":{"file_path":"/x/a.go"}},` +
		`{"type":"tool_use","id":"y","name":"Read","input":{"file_path":"/x/b.go"}}` +
		`]}}`
	writeSession(t, projects, "-Users-lin-dup", "dup", ev, ev)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM tool_targets`); err != nil {
		t.Fatal(err)
	}
	if err := ing.BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 4 messages × 2 facts each = 8 rows; a.go and b.go each appear twice.
	got := activityRows(t, database, "dup")
	want := []string{
		"file|/x/a.go", "file|/x/a.go", "file|/x/b.go", "file|/x/b.go",
		"tool|Read", "tool|Read", "tool|Read", "tool|Read",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("duplicate-event alignment\n got = %v\nwant = %v", got, want)
	}
}

func TestExtractTargetsRedactsCommand(t *testing.T) {
	got := extractTargets("Bash", json.RawMessage(`{"command":"export API_KEY=supersecretvalue123 && go test"}`))
	var cmd string
	for _, g := range got {
		if g.Kind == model.TargetCommand {
			cmd = g.Value
		}
	}
	if cmd == "" {
		t.Fatal("expected a command target")
	}
	if strings.Contains(cmd, "supersecretvalue123") {
		t.Errorf("command target leaked secret: %q", cmd)
	}
	if !strings.Contains(cmd, "REDACTED") {
		t.Errorf("expected a redaction marker in %q", cmd)
	}
}

func TestIngestPopulatesToolTargets(t *testing.T) {
	projects := t.TempDir()
	asst := `{"type":"assistant","timestamp":"2026-04-26T11:00:05Z","sessionId":"sess-tt",` +
		`"message":{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"e1","name":"Edit","input":{"file_path":"/x/form.go"}},` +
		`{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"go test ./..."}},` +
		`{"type":"tool_use","id":"c1","name":"mcp__clio__search","input":{"query":"x"}}` +
		`]}}`
	writeSession(t, projects, "-Users-lin-proj", "sess-tt", asst)

	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}

	rows, err := database.Query(`SELECT kind, value FROM tool_targets WHERE session_uuid='sess-tt'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			t.Fatal(err)
		}
		got = append(got, kind+"|"+value)
	}
	sort.Strings(got)
	want := []string{"command|go test ./...", "file|/x/form.go", "tool|Bash", "tool|Edit"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tool_targets\n got = %v\nwant = %v", got, want)
	}
}

func TestBackfillActivity(t *testing.T) {
	projects := t.TempDir()
	asst := `{"type":"assistant","timestamp":"2026-04-26T11:00:05Z","sessionId":"sess-bf",` +
		`"message":{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"e1","name":"Edit","input":{"file_path":"/x/a.go"}},` +
		`{"type":"tool_use","id":"e2","name":"Edit","input":{"file_path":"/x/b.go"}},` +
		`{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"ls"}}` +
		`]}}`
	writeSession(t, projects, "-Users-lin-bf", "sess-bf", asst)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	// Simulate a database that was indexed before the activity index existed.
	if _, err := database.Exec(`DELETE FROM tool_targets`); err != nil {
		t.Fatal(err)
	}

	if err := ing.BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Order-based matching: the two Edits must map to different files.
	want := []string{"command|ls", "file|/x/a.go", "file|/x/b.go", "tool|Bash", "tool|Edit", "tool|Edit"}
	if got := activityRows(t, database, "sess-bf"); !reflect.DeepEqual(got, want) {
		t.Errorf("after backfill\n got = %v\nwant = %v", got, want)
	}

	// Idempotent: a second run changes nothing.
	if err := ing.BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := activityRows(t, database, "sess-bf"); !reflect.DeepEqual(got, want) {
		t.Errorf("after second backfill\n got = %v\nwant = %v", got, want)
	}
}

// Codex P1 regression: backfill must fill messages whose facts are missing even
// when tool_targets is already non-empty (ingest runs before backfill, so the
// table is rarely empty on a real upgrade). It must not duplicate present facts.
func TestBackfillActivityFillsMissingWithoutDuplicating(t *testing.T) {
	projects := t.TempDir()
	asst := `{"type":"assistant","timestamp":"2026-04-26T11:00:05Z","sessionId":"pm",` +
		`"message":{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"e1","name":"Edit","input":{"file_path":"/x/a.go"}},` +
		`{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"ls"}}` +
		`]}}`
	writeSession(t, projects, "-Users-lin-pm", "pm", asst)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	// Simulate the Edit message predating the activity index (its facts missing),
	// while the Bash message was freshly ingested (facts present). Table is non-empty.
	if _, err := database.Exec(`DELETE FROM tool_targets WHERE kind='file' OR value='Edit'`); err != nil {
		t.Fatal(err)
	}

	if err := ing.BackfillActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"command|ls", "file|/x/a.go", "tool|Bash", "tool|Edit"}
	if got := activityRows(t, database, "pm"); !reflect.DeepEqual(got, want) {
		t.Errorf("backfill of missing facts\n got = %v\nwant = %v", got, want)
	}
}

func TestFullReingestReplacesToolTargets(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-lin-r", "sess-r",
		`{"type":"assistant","timestamp":"2026-04-26T11:00:05Z","sessionId":"sess-r","message":{"role":"assistant","content":[{"type":"tool_use","id":"e1","name":"Edit","input":{"file_path":"/x/a.go"}}]}}`)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	// Force a full re-ingest of the same file: targets must be replaced, not duplicated.
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	want := []string{"file|/x/a.go", "tool|Edit"}
	if got := activityRows(t, database, "sess-r"); !reflect.DeepEqual(got, want) {
		t.Fatalf("after full reingest\n got = %v\nwant = %v", got, want)
	}
}

func TestPurgeRemovesToolTargets(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-Users-lin-p", "sess-p",
		`{"type":"assistant","timestamp":"2026-04-26T11:00:05Z","sessionId":"sess-p","message":{"role":"assistant","content":[{"type":"tool_use","id":"e1","name":"Edit","input":{"file_path":"/x/a.go"}}]}}`)
	database := openTestDB(t)
	ing := New(database, nil)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	if got := activityRows(t, database, "sess-p"); len(got) == 0 {
		t.Fatal("precondition: expected tool_targets after ingest")
	}
	// Delete the source file and reconcile: the session's targets must be purged.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := ing.PurgeMissing(context.Background(), projects); err != nil {
		t.Fatal(err)
	}
	if got := activityRows(t, database, "sess-p"); len(got) != 0 {
		t.Fatalf("expected tool_targets purged, got %v", got)
	}
}

// activityRows returns sorted "kind|value" rows for a session's tool_targets.
func activityRows(t *testing.T, database *db.DB, sess string) []string {
	t.Helper()
	rows, err := database.Query(`SELECT kind, value FROM tool_targets WHERE session_uuid = ?`, sess)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		out = append(out, k+"|"+v)
	}
	sort.Strings(out)
	return out
}
