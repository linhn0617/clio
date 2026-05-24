package cli

import (
	"io"
	"testing"

	"github.com/linhn0617/clio/internal/doctor"
)

func TestReportDoctorReturnsErrorWhenAnyCheckFails(t *testing.T) {
	results := []doctor.Result{
		{Name: "a", OK: true, Detail: "fine"},
		{Name: "b", OK: false, Detail: "bad"},
	}
	if err := reportDoctor(io.Discard, results); err == nil {
		t.Fatal("expected non-nil error when a check failed")
	}
}

func TestReportDoctorReturnsNilWhenAllOK(t *testing.T) {
	results := []doctor.Result{
		{Name: "a", OK: true, Detail: "fine"},
		{Name: "b", OK: true, Detail: "fine"},
	}
	if err := reportDoctor(io.Discard, results); err != nil {
		t.Fatalf("expected nil error when all checks pass, got %v", err)
	}
}
