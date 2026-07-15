package cli

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/ask"
	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/lock"
)

func addAskSession(t *testing.T, d *db.DB, uuid, project string) {
	t.Helper()
	now := time.Now().Unix()
	if _, err := d.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count) VALUES (?,?,?,?,?,1)`,
		uuid, project, uuid+".jsonl", now, now); err != nil {
		t.Fatal(err)
	}
}

func addAskMsg(t *testing.T, d *db.DB, sess string, seq int, role, content string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES (?,?,?,?,?,?)`,
		sess, seq, time.Now().Unix(), role, content, "{}"); err != nil {
		t.Fatal(err)
	}
}

// runAskJSONCapturingStdout sets up a fresh indexed db under a temp
// XDG_DATA_HOME, seeds it, runs `clio ask <question> --json <extraArgs...>`,
// and decodes the emitted bundle. `clio ask --json` encodes straight to
// os.Stdout (internal/cli/ask.go), not cmd.OutOrStdout(), so stdout itself
// must be redirected to capture it.
func runAskJSONCapturingStdout(t *testing.T, seed func(d *db.DB), question string, extraArgs ...string) ask.Answer {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if _, err := config.EnsureDataDir(); err != nil {
		t.Fatal(err)
	}
	dbPath, err := config.DBPath()
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seed(d)
	d.Close()

	// Hold the leader lock so openAndCatchUp (internal/cli/common.go) takes
	// its read-only, no-catch-up path instead of running a real incremental
	// ingest against config.ClaudeProjectsDir() — which, unmocked in this
	// test process, resolves to the machine's actual ~/.claude/projects and
	// would scan real history, not the seeded temp db.
	lockPath, err := config.LockPath()
	if err != nil {
		t.Fatal(err)
	}
	lease, isLeader, err := lock.AcquireOrFollow(lockPath)
	if err != nil || !isLeader {
		t.Fatalf("expected to lead: leader=%v err=%v", isLeader, err)
	}
	defer lease.Release()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	cmd := newAskCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	args := append([]string{question, "--json"}, extraArgs...)
	cmd.SetArgs(args)
	execErr := cmd.Execute()
	w.Close()
	os.Stdout = origStdout

	raw, readErr := io.ReadAll(r)
	if execErr != nil {
		t.Fatalf("ask --json: %v", execErr)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	var a ask.Answer
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("decode --json output: %v\n%s", err, raw)
	}
	return a
}

// TestAskMaxTokensFlagThreadsThroughToGroupCount is the CLI wiring guard,
// mirroring the MCP handler's equivalent test: a --max-tokens value that
// never reaches ask.Options.MaxTokens would make every invocation fall back
// to the package default (2000) regardless of the flag, so this asserts a
// large explicit budget keeps more of 4 equal-sized, budget-straddling
// groups than the (flagless) default does.
func TestAskMaxTokensFlagThreadsThroughToGroupCount(t *testing.T) {
	filler := strings.Repeat("填充內容測試文字說明範例持續資料流程細節描述完整", 30) // >600 CJK runes
	content := "資料庫遷移" + filler
	seed := func(d *db.DB) {
		for _, uuid := range []string{"s1", "s2", "s3", "s4"} {
			addAskSession(t, d, uuid, "/p")
			addAskMsg(t, d, uuid, 0, "user", content)
		}
	}

	def := runAskJSONCapturingStdout(t, seed, "資料庫遷移")
	over := runAskJSONCapturingStdout(t, seed, "資料庫遷移", "--max-tokens", "999999")

	if len(def.Groups) != 3 {
		t.Fatalf("setup: default budget should keep exactly 3 of the 4 ~601-token groups, got %d", len(def.Groups))
	}
	if len(over.Groups) != 4 {
		t.Fatalf("--max-tokens 999999 should keep all 4 groups, got %d — the flag is not reaching ask.Options.MaxTokens", len(over.Groups))
	}
}

// Omitting --max-tokens (or passing 0) must apply the same package default
// (2000) as explicitly passing it.
func TestAskMaxTokensFlagOmittedAppliesDefault(t *testing.T) {
	seed := func(d *db.DB) {
		addAskSession(t, d, "s1", "/p")
		addAskMsg(t, d, "s1", 0, "user", "we keep hitting an authentication failure")
	}

	omitted := runAskJSONCapturingStdout(t, seed, "authentication failure")
	explicit := runAskJSONCapturingStdout(t, seed, "authentication failure", "--max-tokens", "2000")

	if len(omitted.Groups) != len(explicit.Groups) {
		t.Fatalf("omitting --max-tokens should match --max-tokens 2000: %d groups vs %d", len(omitted.Groups), len(explicit.Groups))
	}
}
