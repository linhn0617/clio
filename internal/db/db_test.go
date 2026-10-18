package db

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEscapeLike(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`a%_\b`, `a\%\_\\b`},
		{`plain text`, `plain text`},
		{`%`, `\%`},
		{`_`, `\_`},
		{`\`, `\\`},
		{`no special chars`, `no special chars`},
		{`a%b_c\d`, `a\%b\_c\\d`},
	}
	for _, c := range cases {
		got := EscapeLike(c.in)
		if got != c.want {
			t.Errorf("EscapeLike(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMigration0005IngestStateColumns(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	rows, err := d.Query(`PRAGMA table_info(ingest_state)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	for _, want := range []string{"head_fingerprint", "unparsed_lines"} {
		if !cols[want] {
			t.Errorf("ingest_state missing column %q (have %v)", want, cols)
		}
	}
}

func TestMigrateIsConcurrencySafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.sqlite")

	seed, err := Open(path)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	seed.Close()

	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := Open(path) // Open runs migrate()
			if err != nil {
				errs <- err
				return
			}
			errs <- d.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatalf("concurrent migrate failed: %v", e)
		}
	}
}

// TestMigration0014DropsGeminiRows rebuilds the pre-0014 state on a migrated
// DB (un-record 0014, seed rows, re-run migrate) and checks that every Gemini
// row goes while claude-code rows and FTS stay intact.
func TestMigration0014DropsGeminiRows(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`DELETE FROM schema_migrations WHERE name = '0014_drop_gemini.sql'`)

	seed := func(uuid, source, file string) {
		mustExec(`INSERT INTO sessions(uuid, source_file, source) VALUES (?,?,?)`, uuid, file, source)
		mustExec(`INSERT INTO messages(session_uuid, seq, role, content, raw_json) VALUES (?,0,'user',?,'{}')`, uuid, "needle-"+source)
		mustExec(`INSERT INTO ingest_state(source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, last_ingested_at) VALUES (?,1,1,1,'',1)`, file)
		mustExec(`INSERT INTO session_usage(session_uuid, source, model) VALUES (?,?,'m')`, uuid, source)
	}
	seed("g1", "gemini", "/h/.gemini/tmp/p/chats/session-1.jsonl")
	seed("c1", "claude-code", "/h/.claude/projects/p/c1.jsonl")
	// A non-chats .jsonl under the Gemini root was handed to the claude-code
	// fallback by the old walker: its session and watermark must survive.
	seed("c2", "claude-code", "/h/.gemini/tmp/p/stray.jsonl")
	// Orphaned Gemini watermarks (sessions row already gone), both separators.
	mustExec(`INSERT INTO ingest_state(source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, last_ingested_at) VALUES ('/h/.gemini/tmp/p/chats/orphan.jsonl',1,1,1,'',1)`)
	mustExec(`INSERT INTO ingest_state(source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, last_ingested_at) VALUES ('C:\Users\x\.gemini\tmp\p\chats\orphan.jsonl',1,1,1,'',1)`)
	mustExec(`INSERT INTO source_conflicts(source_file, uuid, seen_source, conflicting_source, first_seen_at, last_seen_at) VALUES ('/x','g1','gemini','claude-code',1,1)`)

	if err := d.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	count := func(q string) int {
		t.Helper()
		var n int
		if err := d.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	for _, c := range []struct {
		q    string
		want int
	}{
		{`SELECT count(*) FROM sessions WHERE source='gemini'`, 0},
		{`SELECT count(*) FROM messages WHERE session_uuid='g1'`, 0},
		{`SELECT count(*) FROM session_usage WHERE source='gemini'`, 0},
		{`SELECT count(*) FROM ingest_state WHERE source_file LIKE '%orphan%'`, 0},
		{`SELECT count(*) FROM ingest_state WHERE source_file = '/h/.gemini/tmp/p/stray.jsonl'`, 1},
		{`SELECT count(*) FROM source_conflicts`, 0},
		{`SELECT count(*) FROM sessions WHERE uuid='c1'`, 1},
		{`SELECT count(*) FROM sessions WHERE uuid='c2'`, 1},
		{`SELECT count(*) FROM messages WHERE session_uuid='c1'`, 1},
		{`SELECT count(*) FROM ingest_state`, 2},
		{`SELECT count(*) FROM session_usage`, 2},
		{`SELECT count(*) FROM messages_fts WHERE messages_fts MATCH '"needle-gemini"'`, 0},
		{`SELECT count(*) FROM messages_fts WHERE messages_fts MATCH '"needle-claude-code"'`, 2},
	} {
		if got := count(c.q); got != c.want {
			t.Errorf("%s = %d, want %d", c.q, got, c.want)
		}
	}
	if _, err := d.Exec(`INSERT INTO messages_fts(messages_fts) VALUES('integrity-check')`); err != nil {
		t.Errorf("fts integrity-check: %v", err)
	}
}

// TestMigration0015DropsRedundantMessagesIndex rebuilds the pre-0015 state (the 0001
// single-column index present, 0015 un-recorded), re-runs migrate, and checks the
// redundant index is gone while every session_uuid lookup still plans onto the
// (session_uuid, seq) unique index that covers it.
func TestMigration0015DropsRedundantMessagesIndex(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	mustExec := func(q string) {
		t.Helper()
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_uuid)`)
	mustExec(`DELETE FROM schema_migrations WHERE name = '0015_drop_redundant_messages_index.sql'`)

	if err := d.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var n int
	if err := d.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_messages_session'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("idx_messages_session still present after 0015")
	}

	for _, q := range []string{
		`SELECT seq FROM messages WHERE session_uuid = 'x' AND seq <= 5 ORDER BY seq DESC LIMIT 3`,
		`SELECT MAX(seq) FROM messages WHERE session_uuid = 'x'`,
		`SELECT id FROM messages WHERE session_uuid = 'x'`,
		`SELECT id FROM messages WHERE session_uuid IN ('x','y')`,
	} {
		rows, err := d.Query(`EXPLAIN QUERY PLAN ` + q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		var plan string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			plan += detail + "\n"
		}
		rows.Close()
		if !strings.Contains(plan, "idx_messages_session_seq") {
			t.Errorf("%s\n plan does not use idx_messages_session_seq:\n%s", q, plan)
		}
	}
}
