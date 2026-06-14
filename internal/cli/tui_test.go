package cli

import (
	"io"
	"strings"
	"testing"
)

// With no index, `clio tui` must not launch the dashboard — it surfaces a clear
// "run clio index" error and exits non-zero.
func TestTUIMissingIndexErrors(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // no clio db under here
	cmd := newTUICmd()
	cmd.SetArgs([]string{})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("tui with no index should error, not launch the dashboard")
	}
	if !strings.Contains(err.Error(), "clio index") {
		t.Fatalf("error should tell the user to run `clio index`, got: %v", err)
	}
}
