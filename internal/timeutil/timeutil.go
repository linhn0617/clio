// Package timeutil parses the relative/absolute time expressions used by both
// the CLI and the MCP server, so the two share one behavior.
package timeutil

import (
	"fmt"
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
	if n := len(s); n >= 2 {
		if num, err := strconv.Atoi(s[:n-1]); err == nil {
			switch s[n-1] {
			case 'd':
				return time.Now().Add(-time.Duration(num) * 24 * time.Hour).Unix(), nil
			case 'h':
				return time.Now().Add(-time.Duration(num) * time.Hour).Unix(), nil
			case 'm':
				return time.Now().Add(-time.Duration(num) * time.Minute).Unix(), nil
			}
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, fmt.Errorf("invalid time %q (use 7d, 12h, 30m, or YYYY-MM-DD)", s)
}
