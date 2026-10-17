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
