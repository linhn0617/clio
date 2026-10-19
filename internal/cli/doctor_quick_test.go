package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/linhn0617/clio/internal/doctor"
)

func TestReportDoctorShowsSkipAndDoesNotWarn(t *testing.T) {
	var buf bytes.Buffer
	err := reportDoctor(&buf, []doctor.Result{
		{Name: "a", OK: true, Detail: "fine"},
		{Name: "fts bloat", OK: true, Detail: "skipped (--quick)", Skipped: true},
	})
	if err != nil {
		t.Fatalf("a skipped check must not fail the run: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "[skip] fts bloat") || !strings.Contains(out, "--quick") {
		t.Fatalf("want a [skip] line mentioning --quick, got:\n%s", out)
	}
	if strings.Contains(out, "WARN") {
		t.Fatalf("unexpected WARN:\n%s", out)
	}
}

func TestDoctorCmdHasQuickFlag(t *testing.T) {
	f := newDoctorCmd().Flags().Lookup("quick")
	if f == nil || f.DefValue != "false" {
		t.Fatalf("doctor must define a --quick bool flag defaulting to false, got %v", f)
	}
}

// The rebuild hint only makes sense for index problems; an integration failure
// already carries its own remedy in the detail.
func TestReportDoctorFooterOnlyForIndexFailures(t *testing.T) {
	const hint = "clio index --full"
	var buf bytes.Buffer
	err := reportDoctor(&buf, []doctor.Result{
		{Name: "hook binary", OK: false, Detail: "hook points to missing binary /x; rerun clio install-hook", Integration: true},
		{Name: "mcp lease", OK: false, Detail: "stale lease", Integration: true},
	})
	if err == nil {
		t.Fatal("integration failures must still fail the run")
	}
	if strings.Contains(buf.String(), hint) {
		t.Fatalf("integration-only failure must not suggest a rebuild:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "WARN") {
		t.Fatalf("missing WARN:\n%s", buf.String())
	}

	buf.Reset()
	_ = reportDoctor(&buf, []doctor.Result{
		{Name: "hook binary", OK: false, Detail: "x", Integration: true},
		{Name: "db integrity", OK: false, Detail: "bad"},
	})
	if !strings.Contains(buf.String(), hint) {
		t.Fatalf("an index failure must keep the rebuild hint:\n%s", buf.String())
	}
}
