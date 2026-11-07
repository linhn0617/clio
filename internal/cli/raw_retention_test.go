package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/db"
)

func putRetention(t *testing.T, d *db.DB, v string) {
	t.Helper()
	if err := d.PutSetting(context.Background(), db.RawRetentionKey, v); err != nil {
		t.Fatal(err)
	}
}

// §4: a stored value that no longer parses keeps raw (fail open) and
// `clio index` warns, naming the value.
func TestIndexWarnsOnBadRetentionAndKeepsRaw(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-old", 30)
	putRetention(t, database, "abc")
	database.Close()
	oldErr := os.Stderr
	rerr, werr, _ := os.Pipe()
	os.Stderr = werr
	_, err := runIndexCmd(t, "--full")
	werr.Close()
	os.Stderr = oldErr
	var eb bytes.Buffer
	eb.ReadFrom(rerr)
	if err != nil {
		t.Fatalf("index --full: %v\n%s", err, eb.String())
	}
	if !strings.Contains(eb.String(), "value=abc") {
		t.Fatalf("no warning naming the bad value:\n%s", eb.String())
	}
	d := reopen(t)
	defer d.Close()
	if raw := rawJSONOf(t, d, "sess-old"); raw[0] == "" {
		t.Fatal("bad retention value blanked raw_json")
	}
}
