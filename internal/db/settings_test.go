package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsRoundTrip(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	if _, ok, err := d.Setting(ctx, RawRetentionKey); err != nil || ok {
		t.Fatalf("fresh db: ok=%v err=%v, want unset", ok, err)
	}
	if err := d.PutSetting(ctx, RawRetentionKey, "14d"); err != nil {
		t.Fatal(err)
	}
	if err := d.PutSetting(ctx, RawRetentionKey, "30d"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := d.Setting(ctx, RawRetentionKey); err != nil || !ok || v != "30d" {
		t.Fatalf("got %q ok=%v err=%v, want 30d", v, ok, err)
	}
	if err := d.DeleteSetting(ctx, RawRetentionKey); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.Setting(ctx, RawRetentionKey); ok {
		t.Fatal("key still set after delete")
	}
}

func TestRawRetention(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	if w, v, err := d.RawRetention(ctx); w != 0 || v != "" || err != nil {
		t.Fatalf("unset: %v %q %v, want 0 \"\" nil", w, v, err)
	}
	d.PutSetting(ctx, RawRetentionKey, "14d")
	if w, v, err := d.RawRetention(ctx); w != 14*24*time.Hour || v != "14d" || err != nil {
		t.Fatalf("14d: %v %q %v", w, v, err)
	}
	// A value written by hand (or by a future clio) that no longer parses is
	// returned by name with the error, so callers can warn and fail open.
	d.PutSetting(ctx, RawRetentionKey, "abc")
	if w, v, err := d.RawRetention(ctx); w != 0 || v != "abc" || err == nil {
		t.Fatalf("bad value: %v %q %v, want 0 \"abc\" error", w, v, err)
	}
}

func TestMigration0017Settings(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`INSERT INTO settings(key, value) VALUES ('k', NULL)`); err == nil {
		t.Fatal("accepted NULL value")
	}
	if _, err := d.Exec(`INSERT INTO settings(key, value) VALUES ('k', 'v')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO settings(key, value) VALUES ('k', 'w')`); err == nil {
		t.Fatal("accepted a duplicate key")
	}
	if err := d.migrate(); err != nil { // re-running migrations keeps the row
		t.Fatal(err)
	}
	var v string
	if err := d.QueryRow(`SELECT value FROM settings WHERE key='k'`).Scan(&v); err != nil || v != "v" {
		t.Fatalf("after re-migrate: %q %v", v, err)
	}
}
