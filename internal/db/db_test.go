package db

import (
	"path/filepath"
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
