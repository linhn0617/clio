package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/ingest"
	"github.com/linhn0617/clio/internal/lock"
)

func runIndexCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	cmd := newIndexCmd()
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	w.Close()
	os.Stdout = old
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, rerr := r.Read(buf)
		b.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	return b.String(), err
}

func TestIndexPurgeMissingDropsRetainedSession(t *testing.T) {
	database, path := pruneSandbox(t, "sess-gone", 1)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	projects := filepath.Dir(filepath.Dir(path))
	if err := ingest.New(database, nil).PurgeMissing(context.Background(), projects); err != nil {
		t.Fatal(err)
	}
	database.Close()

	// A plain index keeps the retained copy.
	if _, err := runIndexCmd(t); err != nil {
		t.Fatal(err)
	}
	d := reopen(t)
	var n int
	d.QueryRow(`SELECT count(*) FROM sessions WHERE uuid='sess-gone'`).Scan(&n)
	d.Close()
	if n != 1 {
		t.Fatalf("plain index dropped the retained session (n=%d)", n)
	}

	out, err := runIndexCmd(t, "--purge-missing")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "purged: 1 session source(s) whose transcript no longer exists") {
		t.Fatalf("purge output:\n%s", out)
	}
	d = reopen(t)
	defer d.Close()
	d.QueryRow(`SELECT count(*) FROM sessions WHERE uuid='sess-gone'`).Scan(&n)
	if n != 0 {
		t.Fatalf("--purge-missing kept the session (n=%d)", n)
	}
	d.QueryRow(`SELECT count(*) FROM ingest_state WHERE source_file=?`, path).Scan(&n)
	if n != 0 {
		t.Fatalf("--purge-missing kept ingest_state (n=%d)", n)
	}
}

func TestIndexPurgeMissingRefusedUnderLock(t *testing.T) {
	database, path := pruneSandbox(t, "sess-gone", 1)
	database.Close()
	os.Remove(path)
	lockPath, err := config.LockPath()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(lockPath), 0o755)
	lease, leader, err := lock.AcquireOrFollow(lockPath)
	if err != nil || !leader {
		t.Fatalf("could not hold lock: %v leader=%v", err, leader)
	}
	defer lease.Release()

	_, err = runIndexCmd(t, "--purge-missing")
	if err == nil || !strings.Contains(err.Error(), "clio index --purge-missing") {
		t.Fatalf("--purge-missing under held lock: err=%v", err)
	}
	d := reopen(t)
	defer d.Close()
	var n int
	d.QueryRow(`SELECT count(*) FROM sessions WHERE uuid='sess-gone'`).Scan(&n)
	if n != 1 {
		t.Fatalf("refused purge still deleted (n=%d)", n)
	}
}

// A large simultaneous disappearance is refused with a --force hint; --force deletes.
func TestIndexPurgeMissingForceOverridesCap(t *testing.T) {
	database, first := pruneSandbox(t, "f00", 1)
	database.Close()
	dir := filepath.Dir(first)
	paths := []string{first}
	for i := 1; i < 20; i++ {
		uuid := fmt.Sprintf("f%02d", i)
		p := filepath.Join(dir, uuid+".jsonl")
		line := `{"type":"user","timestamp":"2026-01-01T10:00:00Z","cwd":"/p","sessionId":"` + uuid + `","message":{"role":"user","content":"hi"}}`
		if err := os.WriteFile(p, []byte(line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if _, err := runIndexCmd(t); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths[:11] {
		os.Remove(p)
	}
	_, err := runIndexCmd(t, "--purge-missing")
	if err == nil || !strings.Contains(err.Error(), "--force") || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Fatalf("mass disappearance: err=%v", err)
	}
	out, err := runIndexCmd(t, "--purge-missing", "--force")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "purged: 11 session source(s)") {
		t.Fatalf("forced purge output:\n%s", out)
	}
}

func TestIndexForceRequiresPurgeMissing(t *testing.T) {
	database, _ := pruneSandbox(t, "f00", 1)
	database.Close()
	_, err := runIndexCmd(t, "--force")
	if err == nil || !strings.Contains(err.Error(), "--purge-missing") {
		t.Fatalf("--force alone: err=%v", err)
	}
}
