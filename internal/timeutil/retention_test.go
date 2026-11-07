package timeutil

import (
	"strings"
	"testing"
	"time"
)

func TestParseRetentionAcceptsRelativeDurations(t *testing.T) {
	cases := map[string]time.Duration{
		"14d":  14 * 24 * time.Hour,
		"12h":  12 * time.Hour,
		"30m":  30 * time.Minute,
		" 7d ": 7 * 24 * time.Hour,
	}
	for in, want := range cases {
		got, err := ParseRetention(in)
		if err != nil || got != want {
			t.Errorf("ParseRetention(%q) = %v, %v; want %v, nil", in, got, err, want)
		}
	}
}

func TestParseRetentionRejectsDatesZeroNegativeGarbage(t *testing.T) {
	for _, bad := range []string{"0d", "-5d", "2026-10-01", "2026-10-01T10:00:00", "abc", "", "1.5d", "14", "d", "999999999999d"} {
		_, err := ParseRetention(bad)
		if err == nil {
			t.Errorf("ParseRetention(%q) accepted, want an error", bad)
			continue
		}
		if !strings.Contains(err.Error(), "positive duration") {
			t.Errorf("ParseRetention(%q) error %q lacks the prune-raw style wording", bad, err)
		}
	}
}
