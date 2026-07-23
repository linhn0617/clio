package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/lock"
)

// A requested --full re-index (e.g. the usage backfill) under a held MCP lock
// must refuse with a non-zero error naming the lock holder — never report the
// silent "nothing to do" success that would skip the backfill.
func TestIndexFullRefusesUnderMCPLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	lockPath, err := config.LockPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	lease, leader, err := lock.AcquireOrFollow(lockPath)
	if err != nil || !leader {
		t.Fatalf("could not become lock leader: leader=%v err=%v", leader, err)
	}
	defer lease.Release()

	cmd := newIndexCmd()
	cmd.SetArgs([]string{"--full"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	execErr := cmd.Execute()
	if execErr == nil {
		t.Fatal("index --full under MCP lock must fail, not succeed silently")
	}
	if !strings.Contains(execErr.Error(), "lock") {
		t.Fatalf("error should name the lock: %v", execErr)
	}

	// Without --full the existing benign behavior stays: success, nothing done.
	cmd2 := newIndexCmd()
	cmd2.SetArgs([]string{})
	cmd2.SilenceUsage, cmd2.SilenceErrors = true, true
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("incremental index under lock should be a benign no-op: %v", err)
	}
}
