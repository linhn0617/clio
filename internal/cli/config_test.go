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
	// While clio mcp runs, `clio index` refuses to run, so the confirmation must
	// also name the MCP leader start that sweeps in that case.
	const setMsg = "raw retention: 14d — raw_json of sessions idle longer than this is not kept; existing sessions are blanked by the next `clio index`, or, while clio mcp is running, when its leader next starts\n"
	if out, err := runConfigCmd(t, "raw-retention", "14d"); err != nil || out != setMsg {
		t.Fatalf("set: out=%q err=%v, want %q", out, err, setMsg)
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

// "off" is matched case-insensitively and around spaces, like the duration it
// replaces, and a rejected value's error names "off" as the way to disable.
func TestConfigRawRetentionOffAnyCase(t *testing.T) {
	database, _ := pruneSandbox(t, "sess-cfg", 1)
	database.Close()
	for _, off := range []string{"OFF", "Off", " off "} {
		if _, err := runConfigCmd(t, "raw-retention", "14d"); err != nil {
			t.Fatal(err)
		}
		if _, err := runConfigCmd(t, "raw-retention", off); err != nil {
			t.Fatalf("raw-retention %q: %v", off, err)
		}
		if out, _ := runConfigCmd(t, "raw-retention"); strings.TrimSpace(out) != "off" {
			t.Fatalf("after %q: out=%q, want \"off\"", off, out)
		}
	}
	_, err := runConfigCmd(t, "raw-retention", "nope")
	if err == nil || !strings.Contains(err.Error(), `"off"`) {
		t.Fatalf("err=%v, want it to name \"off\"", err)
	}
}
