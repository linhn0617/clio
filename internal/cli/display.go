package cli

import (
	"strings"
	"time"

	"github.com/linhn0617/clio/internal/termsafe"
	"github.com/linhn0617/clio/internal/timeutil"
)

// parseSince wraps timeutil.ParseSince so CLI and MCP share one behavior.
func parseSince(s string) (int64, error) {
	return timeutil.ParseSince(s)
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

// oneLine also strips terminal escapes: every caller prints indexed text.
func oneLine(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(termsafe.Strip(s)), "\n", " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func trimProject(p string) string {
	p = termsafe.Strip(p)
	if p == "" {
		return "(unknown)"
	}
	if i := strings.LastIndexByte(p, '/'); i >= 0 && i < len(p)-1 {
		return p[i+1:]
	}
	return p
}
