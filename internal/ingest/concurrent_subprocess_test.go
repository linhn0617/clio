package ingest_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/linhn0617/clio/internal/db"
)

func userJSONLine(uuid string, i int) string {
	return fmt.Sprintf(`{"type":"user","timestamp":"2026-04-26T11:00:00Z","cwd":"/tmp/p","sessionId":%q,"message":{"role":"user","content":"user message %d"}}`, uuid, i)
}

func assistantJSONLine(uuid string, i int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-04-26T11:00:05Z","sessionId":%q,"message":{"role":"assistant","content":[{"type":"text","text":"assistant reply %d"}]}}`, uuid, i)
}

func writeGrowingSession(t *testing.T, path, uuid string, pairs int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i := 0; i < pairs; i++ {
		if _, err := fmt.Fprintln(f, userJSONLine(uuid, i)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(f, assistantJSONLine(uuid, i)); err != nil {
			t.Fatal(err)
		}
	}
}

func buildHelper(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "clio-ingest-once")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/linhn0617/clio/cmd/clio-ingest-once").CombinedOutput()
	if err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}
	return bin
}

func TestCrossProcessConcurrentIngest(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test; skipped in -short")
	}
	root := t.TempDir()
	dbPath := filepath.Join(root, "db.sqlite")
	projects := filepath.Join(root, "projects", "proj")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}

	uuid := "55555555-5555-5555-5555-555555555555"
	file := filepath.Join(projects, uuid+".jsonl")
	writeGrowingSession(t, file, uuid, 200) // 200 user + 200 assistant lines

	bin := buildHelper(t)
	// Isolate the helper from the real ~/.codex: clio-ingest-once registers the Codex
	// source from the home dir, so an empty HOME keeps this test hermetic (CC only).
	home := t.TempDir()

	const procs = 6
	var wg sync.WaitGroup
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(bin)
			cmd.Env = append(os.Environ(), "CLIO_DB="+dbPath, "CLIO_PROJECTS="+filepath.Dir(projects),
				"HOME="+home, "USERPROFILE="+home)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("helper failed: %v\n%s", err, out)
			}
		}()
	}
	wg.Wait()

	d, err := db.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var dupes int
	if err := d.QueryRow(`SELECT COUNT(*) FROM (SELECT session_uuid, seq, COUNT(*) c FROM messages GROUP BY session_uuid, seq HAVING c > 1)`).Scan(&dupes); err != nil {
		t.Fatal(err)
	}
	if dupes != 0 {
		t.Fatalf("found %d duplicate (session_uuid,seq) groups", dupes)
	}

	var msgs, fts int
	if err := d.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&msgs); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM messages_fts`).Scan(&fts); err != nil {
		t.Fatal(err)
	}
	if msgs != fts {
		t.Fatalf("messages=%d fts=%d (must match)", msgs, fts)
	}
	if msgs != 400 {
		t.Fatalf("messages=%d, want 400", msgs)
	}

	var turns, userMsgs int
	if err := d.QueryRow(`SELECT turn_count FROM sessions WHERE uuid=?`, uuid).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_uuid=? AND role='user'`, uuid).Scan(&userMsgs); err != nil {
		t.Fatal(err)
	}
	if turns != userMsgs {
		t.Fatalf("turn_count=%d, user messages=%d (drift)", turns, userMsgs)
	}

	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	var watermark int64
	if err := d.QueryRow(`SELECT last_byte_offset FROM ingest_state WHERE source_file = ?`, file).Scan(&watermark); err != nil {
		t.Fatal(err)
	}
	if watermark != fi.Size() {
		t.Fatalf("watermark=%d, file size=%d (must match)", watermark, fi.Size())
	}
}
