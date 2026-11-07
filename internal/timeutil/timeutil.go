// Package timeutil parses the relative/absolute time expressions used by both
// the CLI and the MCP server, so the two share one behavior.
package timeutil

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ParseSince parses "7d" / "12h" / "30m" (relative to now) or an absolute date
// ("2006-01-02" or "2006-01-02T15:04:05", local time). Empty input returns 0
// (meaning "no lower bound").
func ParseSince(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if num, unit, ok := parseRelative(s); ok {
		return time.Now().Add(-time.Duration(num) * unit).Unix(), nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, fmt.Errorf("invalid time %q (use 7d, 12h, 30m, or YYYY-MM-DD)", s)
}

// parseRelative splits "7d" / "12h" / "30m" into its count and unit. It is the
// relative half of ParseSince, shared with ParseRetention so both accept the
// same spellings.
func parseRelative(s string) (num int, unit time.Duration, ok bool) {
	n := len(s)
	if n < 2 {
		return 0, 0, false
	}
	num, err := strconv.Atoi(s[:n-1])
	if err != nil {
		return 0, 0, false
	}
	switch s[n-1] {
	case 'd':
		return num, 24 * time.Hour, true
	case 'h':
		return num, time.Hour, true
	case 'm':
		return num, time.Minute, true
	}
	return 0, 0, false
}

// ParseRetention parses a rolling retention window ("14d", "12h", "30m") with
// ParseSince's relative grammar. An absolute date is rejected because a rolling
// window has no fixed start; zero, negative and overflowing counts are rejected
// as well.
func ParseRetention(s string) (time.Duration, error) {
	num, unit, ok := parseRelative(strings.TrimSpace(s))
	if !ok || num <= 0 || int64(num) > math.MaxInt64/int64(unit) {
		return 0, fmt.Errorf("raw retention must be a positive duration such as 14d, 12h or 30m (not a date), got %q", s)
	}
	return time.Duration(num) * unit, nil
}

// FormatRFC3339 renders a unix timestamp as local RFC 3339, the time format of
// every MCP tool; 0 (unknown) renders empty.
func FormatRFC3339(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).Format(time.RFC3339)
}
