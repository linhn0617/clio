package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/db"
)

// runConfigCmd runs `clio config <args>` and returns stdout and the error.
func runConfigCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newConfigCmd()
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	return out.String(), err
}

func TestConfigRawRetentionRoundTrip(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-cfg", 1)
	database.Close()

	if out, err := runConfigCmd(t, "raw-retention"); err != nil || strings.TrimSpace(out) != "off" {
		t.Fatalf("unset: out=%q err=%v, want \"off\"", out, err)
	}
	if _, err := runConfigCmd(t, "raw-retention", "14d"); err != nil {
		t.Fatal(err)
	}
	if out, err := runConfigCmd(t, "raw-retention"); err != nil || strings.TrimSpace(out) != "14d" {
		t.Fatalf("after set: out=%q err=%v, want \"14d\"", out, err)
	}
	d := reopen(t)
	v, ok, err := d.Setting(context.Background(), db.RawRetentionKey)
	d.Close()
	if err != nil || !ok || v != "14d" {
		t.Fatalf("stored %q ok=%v err=%v, want 14d", v, ok, err)
	}
	if _, err := runConfigCmd(t, "raw-retention", "off"); err != nil {
		t.Fatal(err)
	}
	if out, _ := runConfigCmd(t, "raw-retention"); strings.TrimSpace(out) != "off" {
		t.Fatalf("after off: out=%q, want \"off\"", out)
	}
}

func TestConfigRawRetentionRejectsBadValues(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-cfg", 1)
	database.Close()
	for _, bad := range []string{"0d", "-5d", "2026-10-01", "abc"} {
		_, err := runConfigCmd(t, "raw-retention", bad)
		if err == nil || !strings.Contains(err.Error(), "positive duration") {
			t.Errorf("raw-retention %q: err=%v, want a positive-duration rejection", bad, err)
		}
	}
	if out, _ := runConfigCmd(t, "raw-retention"); strings.TrimSpace(out) != "off" {
		t.Fatalf("a rejected value was stored: %q", out)
	}
}

func TestRootRegistersConfigRawRetention(t *testing.T) {
	sub, _, err := newRootCmd().Find([]string{"config", "raw-retention"})
	if err != nil || sub.Name() != "raw-retention" {
		t.Fatalf("clio config raw-retention not registered: %v %v", sub, err)
	}
}

func TestConfigRawRetentionHelpStillWorks(t *testing.T) {
	out, err := runConfigCmd(t, "raw-retention", "--help")
	if err != nil || !strings.Contains(out, "clio index --full") {
		t.Fatalf("--help: err=%v out=%q", err, out)
	}
}
