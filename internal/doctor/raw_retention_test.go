package doctor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/db"
)

func TestRunReportsRawRetention(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()

	if r := findResult(t, Run(d, dir, dbPath), "raw retention"); !r.OK || r.Detail != "off" {
		t.Fatalf("unset: %+v, want ok \"off\"", r)
	}
	d.PutSetting(ctx, db.RawRetentionKey, "14d")
	if r := findResult(t, Run(d, dir, dbPath), "raw retention"); !r.OK || r.Detail != "14d" {
		t.Fatalf("14d: %+v, want ok \"14d\"", r)
	}
	d.PutSetting(ctx, db.RawRetentionKey, "abc")
	if r := findResult(t, Run(d, dir, dbPath), "raw retention"); r.OK || !strings.Contains(r.Detail, `"abc"`) {
		t.Fatalf("bad value: %+v, want a warning naming \"abc\"", r)
	}
	// A database last migrated by an older clio (doctor opens read-only and does
	// not migrate) has no settings table: retention is off there.
	if _, err := d.Exec(`ALTER TABLE settings RENAME TO settings_from_the_future`); err != nil {
		t.Fatal(err)
	}
	if r := findResult(t, Run(d, dir, dbPath), "raw retention"); !r.OK || r.Detail != "off" {
		t.Fatalf("no settings table: %+v, want ok \"off\"", r)
	}
}

// The retention line sits next to the pruned raw_json line.
func TestRawRetentionFollowsPrunedLine(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.sqlite")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	results := Run(d, dir, dbPath)
	for i, r := range results {
		if r.Name == "pruned raw_json" {
			if i+1 >= len(results) || results[i+1].Name != "raw retention" {
				t.Fatalf("result after %q is not raw retention: %+v", r.Name, results[i+1:])
			}
			return
		}
	}
	t.Fatal("pruned raw_json line missing")
}
