package watcher

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
)

func msgCount(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	d.QueryRow(`SELECT count(*) FROM messages`).Scan(&n)
	return n
}

func waitFor(t *testing.T, want int, d *db.DB) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if msgCount(t, d) >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d messages, have %d", want, msgCount(t, d))
}

func startWatcher(t *testing.T, projects string, d *db.DB) {
	t.Helper()
	ing := ingest.New(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w := New(ing, projects, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
	time.Sleep(100 * time.Millisecond) // let it register watches
}

func TestWatcherIngestsAppend(t *testing.T) {
	projects := t.TempDir()
	dir := filepath.Join(projects, "-Users-x-p")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "sess-1.jsonl")
	os.WriteFile(path, []byte(line("sess-1", "first message")+"\n"), 0o600)

	d, err := db.Open(filepath.Join(t.TempDir(), "w.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ing := ingest.New(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ing.IngestAll(projects, false) // baseline
	base := msgCount(t, d)

	startWatcher(t, projects, d)

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(line("sess-1", "second message") + "\n")
	f.Close()

	waitFor(t, base+1, d)
}

func TestWatcherPicksUpNewProjectDir(t *testing.T) {
	projects := t.TempDir()
	d, err := db.Open(filepath.Join(t.TempDir(), "w.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	startWatcher(t, projects, d)

	// A brand-new project directory appears after the watcher started.
	dir := filepath.Join(projects, "-Users-x-new")
	os.MkdirAll(dir, 0o755)
	time.Sleep(150 * time.Millisecond) // let the dir-create event register the watch
	os.WriteFile(filepath.Join(dir, "sess-2.jsonl"), []byte(line("sess-2", "hello from new project")+"\n"), 0o600)

	waitFor(t, 1, d)
}

func line(sess, content string) string {
	return `{"type":"user","timestamp":"2026-05-01T10:00:00Z","cwd":"/Users/x/p","sessionId":"` + sess + `","message":{"role":"user","content":"` + content + `"}}`
}
