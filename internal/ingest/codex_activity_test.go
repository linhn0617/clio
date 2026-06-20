package ingest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/model"
	"github.com/linhn0617/clio/internal/sessions"
)

const codexActivityUUID = "0199cccc-dddd-7eee-8fff-aaaaaaaaaaaa"

func equalTargets(a, b []model.ToolTarget) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].Value != b[i].Value {
			return false
		}
	}
	return true
}

func TestCodexExtractTargets(t *testing.T) {
	tool := func(name string) model.ToolTarget { return model.ToolTarget{Kind: model.TargetTool, Value: name} }
	cmd := func(v string) model.ToolTarget { return model.ToolTarget{Kind: model.TargetCommand, Value: v} }
	file := func(v string) model.ToolTarget { return model.ToolTarget{Kind: model.TargetFile, Value: v} }

	cases := []struct {
		name string
		tool string
		args string
		want []model.ToolTarget
	}{
		{"exec_command", "exec_command", `{"cmd":"git status --short","workdir":"/x"}`, []model.ToolTarget{tool("exec_command"), cmd("git status --short")}},
		{"shell bash -lc array", "shell", `{"command":["bash","-lc","go test ./..."],"workdir":"/x"}`, []model.ToolTarget{tool("shell"), cmd("go test ./...")}},
		{"shell /bin/bash", "shell", `{"command":["/bin/bash","-lc","make build"]}`, []model.ToolTarget{tool("shell"), cmd("make build")}},
		{"shell split -l -c", "shell", `{"command":["bash","-l","-c","echo hi"]}`, []model.ToolTarget{tool("shell"), cmd("echo hi")}},
		{"shell string form (legacy fixture)", "shell", `{"command":"ls"}`, []model.ToolTarget{tool("shell"), cmd("ls")}},
		{"shell no -c flag joins argv", "shell", `{"command":["weird","arg1"]}`, []model.ToolTarget{tool("shell"), cmd("weird arg1")}},
		{"shell -c with no script -> tool-only", "shell", `{"command":["bash","-c"]}`, []model.ToolTarget{tool("shell")}},
		{"shell -lc whitespace script -> tool-only", "shell", `{"command":["bash","-lc","   "]}`, []model.ToolTarget{tool("shell")}},
		{"shell non-shell argv0 joins (no -abc false positive)", "shell", `{"command":["weird","-abc","payload"]}`, []model.ToolTarget{tool("shell"), cmd("weird -abc payload")}},
		{"shell interpreter with no command flag joins", "shell", `{"command":["bash","script.sh"]}`, []model.ToolTarget{tool("shell"), cmd("bash script.sh")}},
		{"view_image", "view_image", `{"path":"/repo/diagram.png"}`, []model.ToolTarget{tool("view_image"), file("/repo/diagram.png")}},
		{"update_plan tool-only", "update_plan", `{"plan":[{"status":"pending","step":"x"}]}`, []model.ToolTarget{tool("update_plan")}},
		{"write_stdin tool-only", "write_stdin", `{"session_id":1,"chars":"y"}`, []model.ToolTarget{tool("write_stdin")}},
		{"unknown tool-only", "_fetch_pr", `{"pr_number":3}`, []model.ToolTarget{tool("_fetch_pr")}},
		{"clio mcp -> nil", "mcp__clio__search", `{"query":"x"}`, nil},
		{"empty name -> nil", "", `{}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := codexExtractTargets(c.tool, json.RawMessage(c.args))
			if !equalTargets(got, c.want) {
				t.Fatalf("codexExtractTargets(%q,%s) = %+v, want %+v", c.tool, c.args, got, c.want)
			}
		})
	}
}

func TestCodexExtractTargetsRedactsSecret(t *testing.T) {
	got := codexExtractTargets("exec_command", json.RawMessage(`{"cmd":"deploy --token sk-DEADBEEFDEADBEEFDEADBEEF99"}`))
	if len(got) != 2 {
		t.Fatalf("want [tool, command], got %+v", got)
	}
	if strings.Contains(got[1].Value, "sk-DEADBEEF") {
		t.Fatalf("secret not redacted in command target: %q", got[1].Value)
	}
	if !strings.Contains(got[1].Value, "REDACTED") {
		t.Fatalf("expected redaction marker in command target: %q", got[1].Value)
	}
}

// TestCodexToolSummaryRedactsBeforeTruncate proves the summary redacts the full
// value BEFORE truncating: a secret straddling the 200-rune firstLine boundary
// would leak as a partial (regex-missed) token under a truncate-first order.
func TestCodexToolSummaryRedactsBeforeTruncate(t *testing.T) {
	cmd := strings.Repeat("a", 190) + " sk-DEADBEEFDEADBEEFDEADBEEF99"
	args, _ := json.Marshal(map[string]string{"cmd": cmd})
	got := codexToolSummary("exec_command", json.RawMessage(args))
	if strings.Contains(got, "sk-DEADBEEF") {
		t.Fatalf("partial secret leaked; redaction must run before truncation: %q", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Fatalf("expected redaction marker in summary: %q", got)
	}
}

func TestCodexToolSummaryShowsCommand(t *testing.T) {
	if got := codexToolSummary("exec_command", json.RawMessage(`{"cmd":"git status --short"}`)); got != "git status --short" {
		t.Fatalf("summary=%q want %q", got, "git status --short")
	}
	if got := codexToolSummary("update_plan", json.RawMessage(`{"plan":[]}`)); got != "" {
		t.Fatalf("tool-only summary=%q want empty", got)
	}
}

func TestCodexActivityTargetsEndToEnd(t *testing.T) {
	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(codexSource{root: "testdata/codex_activity"})
	emptyCC := t.TempDir()
	if _, err := ing.IngestAll(context.Background(), emptyCC, false); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	cmds, err := sessions.ActivityByKind(ctx, database, "command", 0, "", "codex", 50)
	if err != nil {
		t.Fatal(err)
	}
	gotCmds := map[string]bool{}
	for _, c := range cmds {
		gotCmds[c.Value] = true
		if strings.Contains(c.Value, "sk-DEADBEEF") {
			t.Fatalf("secret leaked into a codex command target: %q", c.Value)
		}
	}
	for _, want := range []string{"git status --short", "go test ./..."} {
		if !gotCmds[want] {
			t.Fatalf("command %q not surfaced by ActivityByKind(codex); got %v", want, gotCmds)
		}
	}

	def, err := sessions.ActivityByKind(ctx, database, "command", 0, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range def {
		if c.Value == "git status --short" {
			t.Fatal("codex command leaked into the default claude-code source")
		}
	}

	files, err := sessions.ActivityByKind(ctx, database, "file", 0, "", "codex", 50)
	if err != nil {
		t.Fatal(err)
	}
	foundFile := false
	for _, f := range files {
		if f.Value == "/repo/diagram.png" {
			foundFile = true
		}
	}
	if !foundFile {
		t.Fatalf("view_image path not surfaced as a codex file target; got %v", files)
	}

	var content string
	if err := database.QueryRow(`SELECT content FROM messages WHERE session_uuid=? AND role='tool_use' AND content LIKE 'exec_command git%' LIMIT 1`, codexActivityUUID).Scan(&content); err != nil {
		t.Fatalf("no tool_use message with the command summary: %v", err)
	}
	if !strings.Contains(content, "git status --short") {
		t.Fatalf("tool_use content %q does not include the command (summary fix)", content)
	}

	// clio's own MCP traffic from Codex must not be indexed (mirror the Claude path:
	// skip the tool_use and its matching tool_result).
	var clioMsgs int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=? AND content LIKE 'mcp__clio__%'`, codexActivityUUID).Scan(&clioMsgs)
	if clioMsgs != 0 {
		t.Fatalf("clio MCP tool_use indexed for codex: want 0, got %d", clioMsgs)
	}
	var clioResults int
	database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=? AND content LIKE '%CLIO_MCP_RESULT_SHOULD_NOT_BE_INDEXED%'`, codexActivityUUID).Scan(&clioResults)
	if clioResults != 0 {
		t.Fatalf("clio MCP tool_result indexed for codex: want 0, got %d", clioResults)
	}
}
