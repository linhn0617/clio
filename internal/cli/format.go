package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseSince parses "7d", "12h", "30m", or an absolute date "2006-01-02" /
// "2006-01-02T15:04:05" into a unix timestamp. Empty returns 0 (no bound).
func parseSince(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if n := len(s); n >= 2 {
		unit := s[n-1]
		if num, err := strconv.Atoi(s[:n-1]); err == nil {
			var d time.Duration
			switch unit {
			case 'd':
				d = time.Duration(num) * 24 * time.Hour
			case 'h':
				d = time.Duration(num) * time.Hour
			case 'm':
				d = time.Duration(num) * time.Minute
			default:
				d = -1
			}
			if d >= 0 {
				return time.Now().Add(-d).Unix(), nil
			}
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, fmt.Errorf("invalid --since %q (use 7d, 12h, 30m, or YYYY-MM-DD)", s)
}

func shortID(uuid string) string {
	if len(uuid) > 8 {
		return uuid[:8]
	}
	return uuid
}

func formatTS(ts int64) string {
	if ts == 0 {
		return "                "
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}

func oneLine(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func trimProject(p string) string {
	if p == "" {
		return "(unknown)"
	}
	if i := strings.LastIndexByte(p, '/'); i >= 0 && i < len(p)-1 {
		return p[i+1:]
	}
	return p
}
