package ingest

import (
	"context"
	"testing"
)

const codexTestUUID = "0199aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee"

func TestCodexSourceIngest(t *testing.T) {
	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(codexSource{root: "testdata/codex"})
	emptyCC := t.TempDir() // no Claude Code sessions; codex root supplies the files
	if _, err := ing.IngestAll(context.Background(), emptyCC, false); err != nil {
		t.Fatal(err)
	}

	var src, pp string
	var turns int
	if err := database.QueryRow(`SELECT source, COALESCE(project_path,''), turn_count FROM sessions WHERE uuid=?`, codexTestUUID).Scan(&src, &pp, &turns); err != nil {
		t.Fatalf("codex session not indexed: %v", err)
	}
	if src != "codex" {
		t.Fatalf("source=%q want codex", src)
	}
	if pp != "/Users/dev/myproj" {
		t.Fatalf("project_path=%q want /Users/dev/myproj (from session_meta cwd)", pp)
	}
	if turns != 1 {
		t.Fatalf("turn_count=%d want 1 (wrapper-only user + event_msg duplicate must not count)", turns)
	}

	roleCount := func(role string) int {
		var n int
		database.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=? AND role=?`, codexTestUUID, role).Scan(&n)
		return n
	}
	if roleCount("user") != 1 {
		t.Fatalf("user messages=%d want 1 (event_msg stream must not double-count)", roleCount("user"))
	}
	var userContent string
	database.QueryRow(`SELECT content FROM messages WHERE session_uuid=? AND role='user'`, codexTestUUID).Scan(&userContent)
	if userContent != "add a codex source adapter" {
		t.Fatalf("user content=%q want 'add a codex source adapter' (env-context wrapper stripped)", userContent)
	}
	if roleCount("assistant") != 1 {
		t.Fatalf("assistant=%d want 1", roleCount("assistant"))
	}
	if roleCount("tool_use") != 1 {
		t.Fatalf("tool_use=%d want 1", roleCount("tool_use"))
	}
	if roleCount("tool_result") != 1 {
		t.Fatalf("tool_result=%d want 1", roleCount("tool_result"))
	}
	if roleCount("thinking") != 0 {
		t.Fatalf("thinking=%d want 0 (reasoning summary is empty)", roleCount("thinking"))
	}

	var title string
	database.QueryRow(`SELECT COALESCE(title,'') FROM sessions WHERE uuid=?`, codexTestUUID).Scan(&title)
	if title != "add a codex source adapter" {
		t.Fatalf("title=%q want 'add a codex source adapter'", title)
	}
}

func TestCodexSessionIDFromPath(t *testing.T) {
	s := codexSource{}
	got := s.SessionIDFromPath("/x/.codex/sessions/2026/06/19/rollout-2026-06-19T10-00-00-0199aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee.jsonl")
	if got != codexTestUUID {
		t.Fatalf("SessionIDFromPath=%q want %q", got, codexTestUUID)
	}
}
