package ingest

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"
)

// fastSettle keeps the retry pacing of a forced ingest short in tests.
func fastSettle(ing *Ingester) {
	ing.settle = settlePolicy{retries: 3, quiet: 20 * time.Millisecond, poll: 5 * time.Millisecond, maxWait: 200 * time.Millisecond}
}

// appendFromGoroutine is appendLine for a writer goroutine, where t.Fatal must
// not be called.
func appendFromGoroutine(t *testing.T, path, line string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Error(err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Error(err)
	}
}

func countMessages(t *testing.T, ing *Ingester, uuid string) int {
	t.Helper()
	var n int
	if err := ing.db.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid = ?`, uuid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A file that grows once while a full ingest reads it is retried after it
// settles and committed with the grown content.
func TestFullIngestRetriesFileThatSettles(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-p", "sess-1", evUser1, evUser2)
	ing := New(openTestDB(t), nil)
	fastSettle(ing)
	ing.preCommitHook = func() {
		ing.preCommitHook = nil
		appendLine(t, path, evAsst1)
	}
	t.Cleanup(func() { ing.preCommitHook = nil })

	st, err := ing.IngestAll(context.Background(), projects, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.FilesIngested != 1 || st.FilesSkipped != 0 || len(st.StillChanging) != 0 {
		t.Fatalf("stats = %+v, want the file ingested on retry", st)
	}
	var asst int
	if err := ing.db.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid = 'sess-1' AND role = 'assistant'`).Scan(&asst); err != nil {
		t.Fatal(err)
	}
	if asst == 0 {
		t.Fatal("the line appended during the first attempt was not ingested")
	}
}

// A file that changes before every commit is given up on after the retries,
// named in the stats, and left exactly as it was indexed before.
func TestFullIngestGivesUpOnFileThatNeverSettles(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-p", "sess-1", evUser1, evUser2)
	ing := New(openTestDB(t), nil)
	fastSettle(ing)
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	before := countMessages(t, ing, "sess-1")
	prior, err := ing.loadState(path)
	if err != nil || prior == nil {
		t.Fatalf("seed state: %v %v", prior, err)
	}

	commits := 0
	ing.preCommitHook = func() {
		commits++
		appendLine(t, path, evUser2)
	}
	t.Cleanup(func() { ing.preCommitHook = nil })

	st, err := ing.IngestAll(context.Background(), projects, true)
	if err != nil {
		t.Fatal(err)
	}
	if commits != 1+ing.settle.retries {
		t.Errorf("commit attempts = %d, want %d", commits, 1+ing.settle.retries)
	}
	if st.FilesSkipped != 1 || st.FilesIngested != 0 || !slices.Equal(st.StillChanging, []string{path}) {
		t.Fatalf("stats = %+v, want the file skipped and listed", st)
	}
	if got := countMessages(t, ing, "sess-1"); got != before {
		t.Errorf("messages = %d, want %d unchanged", got, before)
	}
	after, err := ing.loadState(path)
	if err != nil || after == nil || *after != *prior {
		t.Errorf("ingest_state changed: %+v -> %+v (%v)", prior, after, err)
	}
}

// An incremental pass does not retry: its next pass resumes from the stored
// offset anyway.
func TestIncrementalIngestDoesNotRetry(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-p", "sess-1", evUser1)
	ing := New(openTestDB(t), nil)
	fastSettle(ing)
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		t.Fatal(err)
	}
	appendLine(t, path, evUser2)

	commits := 0
	ing.preCommitHook = func() {
		commits++
		appendLine(t, path, evAsst1)
	}
	t.Cleanup(func() { ing.preCommitHook = nil })

	st, err := ing.IngestAll(context.Background(), projects, false)
	if err != nil {
		t.Fatal(err)
	}
	if commits != 1 {
		t.Errorf("commit attempts = %d, want 1", commits)
	}
	if len(st.StillChanging) != 0 {
		t.Errorf("StillChanging = %v, want none on an incremental pass", st.StillChanging)
	}
}

// waitSettled returns only once the writes have stopped for the quiet period.
func TestWaitSettledWaitsForQuiet(t *testing.T) {
	path := writeSession(t, t.TempDir(), "-p", "sess-1", evUser1)
	ing := New(openTestDB(t), nil)
	// quiet is 30x the write gap, so a scheduling stall does not pass for quiet.
	ing.settle = settlePolicy{quiet: 300 * time.Millisecond, poll: 2 * time.Millisecond, maxWait: 10 * time.Second}
	done := make(chan struct{})
	defer func() { <-done }() // the writer must not outlive the test
	var lastWrite time.Time   // read only after done is closed
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			time.Sleep(10 * time.Millisecond)
			lastWrite = time.Now() // before the write, so no poll can see it earlier
			appendFromGoroutine(t, path, evUser2)
		}
	}()
	start := time.Now()
	settled, err := ing.waitSettled(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	returned := time.Now()
	if !settled {
		t.Errorf("reported not settled after %v", returned.Sub(start))
	}
	select {
	case <-done:
	default:
		t.Errorf("returned after %v while the file was still being written", returned.Sub(start))
		return
	}
	// A wait that just sleeps for quiet from the start would return before
	// the file had been quiet that long.
	if quietFor := returned.Sub(lastWrite); quietFor < ing.settle.quiet {
		t.Errorf("returned %v after the last write, want at least %v", quietFor, ing.settle.quiet)
	}
}

// A file that never stops changing is waited on for maxWait, then reported as
// not settled.
func TestWaitSettledGivesUpAfterMaxWait(t *testing.T) {
	path := writeSession(t, t.TempDir(), "-p", "sess-1", evUser1)
	ing := New(openTestDB(t), nil)
	ing.settle = settlePolicy{quiet: time.Second, poll: 2 * time.Millisecond, maxWait: 80 * time.Millisecond}
	stop := make(chan struct{})
	writer := make(chan struct{})
	go func() {
		defer close(writer)
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				appendFromGoroutine(t, path, evUser2)
			}
		}
	}()
	// The deadline turns a wait that never gives up into a failure, not a hang.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	settled, err := ing.waitSettled(ctx, path)
	elapsed := time.Since(start)
	close(stop)
	<-writer
	if err != nil {
		t.Fatal(err)
	}
	if settled {
		t.Error("reported settled for a file written every 5ms")
	}
	if elapsed < 80*time.Millisecond || elapsed > 900*time.Millisecond {
		t.Errorf("waited %v, want about maxWait (80ms)", elapsed)
	}
}

// A file that disappears while waiting is an error, not a retry.
func TestWaitSettledMissingFile(t *testing.T) {
	ing := New(openTestDB(t), nil)
	fastSettle(ing)
	if _, err := ing.waitSettled(context.Background(), t.TempDir()+"/gone.jsonl"); err == nil {
		t.Fatal("want an error for a missing file")
	}
}

// A forced ingest waits for the file to go quiet before each retry, so a
// writer that keeps going for a while does not use up the retries at once.
func TestFullIngestWaitsBeforeRetrying(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-p", "sess-1", evUser1, evUser2)
	ing := New(openTestDB(t), nil)
	ing.settle = settlePolicy{retries: 3, quiet: 100 * time.Millisecond, poll: 5 * time.Millisecond, maxWait: time.Second}
	var until time.Time
	ing.preCommitHook = func() {
		if until.IsZero() {
			until = time.Now().Add(150 * time.Millisecond) // the writer stays busy this long
		}
		if time.Now().Before(until) {
			appendLine(t, path, evAsst1)
		}
	}
	t.Cleanup(func() { ing.preCommitHook = nil })

	st, err := ing.IngestAll(context.Background(), projects, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.FilesIngested != 1 || len(st.StillChanging) != 0 {
		t.Fatalf("stats = %+v, want the file ingested once the writer stopped", st)
	}
}

// Each retry of a forced ingest rebuilds the file from scratch, so rows an
// older ingest left behind are replaced even when the file is long enough to
// resume from its stored offset.
func TestFullIngestRetryRebuildsFromScratch(t *testing.T) {
	projects := t.TempDir()
	lines := []string{evUser1}
	for i := 0; i < 8; i++ { // well past fingerprintWindow, so an incremental pass would resume
		lines = append(lines, evUser2)
	}
	path := writeSession(t, projects, "-p", "sess-1", lines...)
	ing := New(openTestDB(t), nil)
	fastSettle(ing)
	if _, _, err := ing.IngestFile(context.Background(), path, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ing.db.Exec(`UPDATE messages SET content = 'OLDROW' WHERE session_uuid = 'sess-1'`); err != nil {
		t.Fatal(err)
	}
	ing.preCommitHook = func() {
		ing.preCommitHook = nil
		appendLine(t, path, evAsst1)
	}
	t.Cleanup(func() { ing.preCommitHook = nil })

	if _, err := ing.IngestAll(context.Background(), projects, true); err != nil {
		t.Fatal(err)
	}
	var old int
	if err := ing.db.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid = 'sess-1' AND content = 'OLDROW'`).Scan(&old); err != nil {
		t.Fatal(err)
	}
	if old != 0 {
		t.Fatalf("%d rows from the earlier ingest survived the retried full re-ingest", old)
	}
}

// A file removed while a forced ingest waits to retry it is skipped, as a
// single attempt would skip it, and not reported as still changing.
func TestFullIngestSkipsFileRemovedWhileWaiting(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-p", "sess-1", evUser1, evUser2)
	ing := New(openTestDB(t), nil)
	fastSettle(ing)
	ing.preCommitHook = func() {
		ing.preCommitHook = nil
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { ing.preCommitHook = nil })

	st, err := ing.IngestAll(context.Background(), projects, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.FilesSkipped != 1 || st.FilesIngested != 0 || len(st.StillChanging) != 0 {
		t.Fatalf("stats = %+v, want the removed file counted as skipped only", st)
	}
}

// Cancelling the context ends the wait at once.
func TestWaitSettledStopsOnCancel(t *testing.T) {
	path := writeSession(t, t.TempDir(), "-p", "sess-1", evUser1)
	ing := New(openTestDB(t), nil)
	ing.settle = settlePolicy{quiet: time.Second, poll: 2 * time.Millisecond, maxWait: 5 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	start := time.Now()
	_, err := ing.waitSettled(ctx, path)
	if err == nil {
		t.Fatal("want the context's error")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("waited %v after cancel, want an immediate return", elapsed)
	}
}

// A file that keeps changing through the whole wait is given up on at once,
// not re-read: a re-read of a file still being written would only go stale again.
func TestFullIngestGivesUpWhenWaitDoesNotSettle(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-p", "sess-1", evUser1, evUser2)
	ing := New(openTestDB(t), nil)
	ing.settle = settlePolicy{retries: 3, quiet: time.Second, poll: 2 * time.Millisecond, maxWait: 60 * time.Millisecond}
	stop := make(chan struct{})
	writer := make(chan struct{})
	commits := 0
	ing.preCommitHook = func() {
		commits++
		if commits > 1 {
			return
		}
		appendLine(t, path, evAsst1)
		go func() { // keeps the file changing for the whole wait
			defer close(writer)
			for {
				select {
				case <-stop:
					return
				case <-time.After(5 * time.Millisecond):
					appendFromGoroutine(t, path, evUser2)
				}
			}
		}()
	}
	t.Cleanup(func() { ing.preCommitHook = nil })

	st, err := ing.IngestAll(context.Background(), projects, true)
	close(stop)
	if commits > 0 {
		<-writer
	}
	if err != nil {
		t.Fatal(err)
	}
	if commits != 1 {
		t.Errorf("commit attempts = %d, want 1 (no re-read of a file that did not settle)", commits)
	}
	if st.FilesSkipped != 1 || !slices.Equal(st.StillChanging, []string{path}) {
		t.Fatalf("stats = %+v, want the file skipped and listed", st)
	}
}

// A file removed during the last attempt is skipped like any removed file and
// not listed as still changing: there is nothing left to re-index.
func TestFullIngestFileRemovedOnLastAttemptNotListed(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-p", "sess-1", evUser1, evUser2)
	ing := New(openTestDB(t), nil)
	fastSettle(ing)
	commits := 0
	ing.preCommitHook = func() {
		commits++
		if commits <= ing.settle.retries {
			appendLine(t, path, evUser2)
			return
		}
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { ing.preCommitHook = nil })

	st, err := ing.IngestAll(context.Background(), projects, true)
	if err != nil {
		t.Fatal(err)
	}
	if commits != 1+ing.settle.retries {
		t.Errorf("commit attempts = %d, want %d", commits, 1+ing.settle.retries)
	}
	if st.FilesSkipped != 1 || st.FilesIngested != 0 || len(st.StillChanging) != 0 {
		t.Fatalf("stats = %+v, want the removed file counted as skipped only", st)
	}
}

// Cancelling while a forced ingest waits to retry the last file ends the pass
// with the context's error, not a success that hides the interruption.
func TestFullIngestCancelDuringWaitReturnsError(t *testing.T) {
	projects := t.TempDir()
	path := writeSession(t, projects, "-p", "sess-1", evUser1, evUser2)
	ing := New(openTestDB(t), nil)
	ing.settle = settlePolicy{retries: 3, quiet: 5 * time.Second, poll: 2 * time.Millisecond, maxWait: 10 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ing.preCommitHook = func() {
		ing.preCommitHook = nil
		appendLine(t, path, evAsst1)
		time.AfterFunc(30*time.Millisecond, cancel)
	}
	t.Cleanup(func() { ing.preCommitHook = nil })

	start := time.Now()
	_, err := ing.IngestAll(ctx, projects, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("returned after %v, want soon after the cancel", elapsed)
	}
}
