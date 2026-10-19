package watcher

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
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
	ing.IngestAll(context.Background(), projects, false) // baseline
	base := msgCount(t, d)

	startWatcher(t, projects, d)

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(line("sess-1", "second message") + "\n")
	f.Close()

	waitFor(t, base+1, d)
}

// The backstop keeps a deleted transcript's indexed copy (Claude Code's
// cleanupPeriodDays must not erase clio's history) and only marks it missing.
func TestWatcherBackstopRetainsDeleted(t *testing.T) {
	orig := backstopPeriod
	backstopPeriod = 200 * time.Millisecond
	t.Cleanup(func() { backstopPeriod = orig })

	projects := t.TempDir()
	dir := filepath.Join(projects, "-Users-x-p")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "del-1.jsonl")
	os.WriteFile(path, []byte(line("del-1", "to be deleted")+"\n"), 0o600)

	d, err := db.Open(filepath.Join(t.TempDir(), "w.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ing := ingest.New(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ing.IngestAll(context.Background(), projects, false)
	if msgCount(t, d) == 0 {
		t.Fatal("baseline ingest produced no messages")
	}

	startWatcher(t, projects, d)
	os.Remove(path)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var marked int
		d.QueryRow(`SELECT count(*) FROM ingest_state WHERE source_file=? AND source_missing_at IS NOT NULL`, path).Scan(&marked)
		if marked == 1 {
			var n int
			d.QueryRow(`SELECT count(*) FROM sessions WHERE uuid='del-1'`).Scan(&n)
			if n != 1 {
				t.Fatalf("backstop deleted the retained session (n=%d)", n)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("deleted source not marked missing by backstop")
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

// The backstop walk must run under the caller's guard: a guard that refuses
// (returns nil) skips the whole round, and one that grants runs it and is released.
func TestWatcherBackstopHonorsGuard(t *testing.T) {
	orig := backstopPeriod
	backstopPeriod = 100 * time.Millisecond
	t.Cleanup(func() { backstopPeriod = orig })

	projects := t.TempDir()
	dir := filepath.Join(projects, "-Users-x-p")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "g-1.jsonl")
	os.WriteFile(path, []byte(line("g-1", "guarded")+"\n"), 0o600)

	d, err := db.Open(filepath.Join(t.TempDir(), "w.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ing := ingest.New(d, log)
	ing.IngestAll(context.Background(), projects, false)
	os.Remove(path)

	var calls, released atomic.Int32
	var grant atomic.Bool
	w := New(ing, projects, log)
	w.BackstopGuard = func() func() {
		calls.Add(1)
		if !grant.Load() {
			return nil
		}
		return func() { released.Add(1) }
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	marked := func() int {
		var n int
		d.QueryRow(`SELECT count(*) FROM ingest_state WHERE source_file=? AND source_missing_at IS NOT NULL`, path).Scan(&n)
		return n
	}
	time.Sleep(600 * time.Millisecond)
	if calls.Load() == 0 {
		t.Fatal("guard never consulted by the backstop")
	}
	if marked() != 0 {
		t.Fatal("backstop ran although the guard refused")
	}
	grant.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for marked() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if marked() == 0 {
		t.Fatal("backstop did not run once the guard granted")
	}
	if released.Load() == 0 {
		t.Fatal("guard release never called")
	}
}
