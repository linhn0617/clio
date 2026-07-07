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

// TestCodexSourceIngestSessionMetaMismatch covers a Codex rollout file whose
// session_meta.id disagrees with the filename uuid (corruption, or a
// session_meta carried over from a different run). The filename uuid is
// canonical: the file must still be indexed under it, not dropped.
func TestCodexSourceIngestSessionMetaMismatch(t *testing.T) {
	const mismatchUUID = "0199bbbb-cccc-7ddd-8eee-111111111111"

	database := openTestDB(t)
	ing := New(database, nil)
	ing.AddSource(codexSource{root: "testdata/codex"})
	emptyCC := t.TempDir()
	if _, err := ing.IngestAll(context.Background(), emptyCC, false); err != nil {
		t.Fatal(err)
	}

	var src, pp string
	if err := database.QueryRow(`SELECT source, COALESCE(project_path,'') FROM sessions WHERE uuid=?`, mismatchUUID).Scan(&src, &pp); err != nil {
		t.Fatalf("codex session with mismatched session_meta.id not indexed under filename uuid: %v", err)
	}
	if src != "codex" {
		t.Fatalf("source=%q want codex", src)
	}
	if pp != "/Users/dev/otherproj" {
		t.Fatalf("project_path=%q want /Users/dev/otherproj (from session_meta cwd)", pp)
	}

	var userContent string
	if err := database.QueryRow(`SELECT content FROM messages WHERE session_uuid=? AND role='user'`, mismatchUUID).Scan(&userContent); err != nil {
		t.Fatalf("user message not indexed under filename uuid: %v", err)
	}
	if userContent != "mismatched session_meta id should not drop this file" {
		t.Fatalf("user content=%q want 'mismatched session_meta id should not drop this file'", userContent)
	}

	// The mismatched session_meta.id must never appear as a session uuid.
	var n int
	database.QueryRow(`SELECT COUNT(*) FROM sessions WHERE uuid='0199bbbb-cccc-7ddd-8eee-222222222222'`).Scan(&n)
	if n != 0 {
		t.Fatalf("session_meta.id must not be used as session uuid when it mismatches the filename; found %d rows", n)
	}
}

func TestCodexSessionIDFromPath(t *testing.T) {
	s := codexSource{}
	got := s.SessionIDFromPath("/x/.codex/sessions/2026/06/19/rollout-2026-06-19T10-00-00-0199aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee.jsonl")
	if got != codexTestUUID {
		t.Fatalf("SessionIDFromPath=%q want %q", got, codexTestUUID)
	}
}
