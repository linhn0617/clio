package cli

import (
	"strings"
	"testing"
)

func TestShortIDStripsTerminalEscapes(t *testing.T) {
	got := shortID("\x1bcBADBADBAD")
	if strings.ContainsRune(got, 0x1b) || got != "BADBADBA" {
		t.Fatalf("shortID = %q, want escape-free %q", got, "BADBADBA")
	}
}
