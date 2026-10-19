package cli

import (
	"errors"
	"strings"
	"time"

	"github.com/linhn0617/clio/internal/sessions"
	"github.com/linhn0617/clio/internal/termsafe"
	"github.com/linhn0617/clio/internal/timeutil"
)

// parseSince wraps timeutil.ParseSince so CLI and MCP share one behavior.
func parseSince(s string) (int64, error) {
	return timeutil.ParseSince(s)
}

func shortID(uuid string) string {
	uuid = termsafe.Strip(uuid)
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
func oneLine(s string, max int) string { return termsafe.OneLine(s, max) }

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

// resolveError renders an ambiguous session id with the same time and title
// formatting as `clio list`.
func resolveError(err error) error {
	var amb *sessions.AmbiguousError
	if errors.As(err, &amb) {
		return errors.New(amb.Render(formatTS, func(t string) string { return oneLine(t, 60) }))
	}
	return err
}
