package doctor

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/linhn0617/clio/internal/claudeconfig"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/lock"
	"github.com/linhn0617/clio/internal/shellwords"
)

// staleIndexAge is how old the newest ingest may be before doctor warns. The MCP
// server (or install-hook's recall) normally keeps the index within minutes; a day
// without any ingest means nothing is feeding it.
const staleIndexAge = 24 * time.Hour

// binaryProblem returns "" when the first word of a hook/MCP command names an
// existing executable, otherwise a short reason. A bare name (no slash) is looked
// up on PATH the way a shell would.
func binaryProblem(path string) string {
	if !strings.ContainsRune(path, os.PathSeparator) {
		if _, err := exec.LookPath(path); err != nil {
			return "missing binary " + path
		}
		return ""
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "missing binary " + path
	}
	if fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
		return "binary not executable: " + path
	}
	return ""
}

// firstWord is the program a hook command runs. shellwords handles the quoting
// install-hook writes for paths with spaces; if it cannot parse, fall back to
// whitespace splitting rather than skipping the check.
func firstWord(command string) string {
	if w, ok := shellwords.Split(command); ok && len(w) > 0 {
		return w[0]
	}
	if f := strings.Fields(command); len(f) > 0 {
		return f[0]
	}
	return ""
}

// checkHookBinary verifies the binaries clio's recall and file-history hooks point
// at. A reinstall that moves or deletes the binary leaves settings.json pointing at
// nothing, and the hook then fails silently on every session.
func checkHookBinary(settingsPath string) Result {
	const name = "hook binary"
	recall, fh, err := claudeconfig.ClioHookCommands(settingsPath)
	if err != nil {
		return Result{Name: name, OK: false, Detail: "cannot read " + settingsPath + ": " + err.Error()}
	}
	cmds := append(append([]string{}, recall...), fh...)
	if len(cmds) == 0 {
		return Result{Name: name, OK: true, Detail: "not installed (clio install-hook)"}
	}
	var problems, good []string
	seen := map[string]bool{}
	for _, c := range cmds {
		bin := firstWord(c)
		if seen[bin] {
			continue
		}
		seen[bin] = true
		if p := binaryProblem(bin); p != "" {
			problems = append(problems, p)
		} else {
			good = append(good, bin)
		}
	}
	if len(problems) > 0 {
		var parts []string
		for _, p := range problems {
			parts = append(parts, "hook points to "+p)
		}
		return Result{Name: name, OK: false, Detail: strings.Join(parts, "; ") + "; rerun clio install-hook"}
	}
	return Result{Name: name, OK: true, Detail: strings.Join(good, ", ")}
}

// checkMCPRegistration reports whether clio is registered as an MCP server and, if
// so, whether its command still exists. Not registered is fine: MCP is optional.
func checkMCPRegistration(configPath string) Result {
	const name = "mcp registration"
	cmd, ok, err := claudeconfig.ServerCommand(configPath, "clio")
	if err != nil {
		return Result{Name: name, OK: false, Detail: "cannot read " + configPath + ": " + err.Error()}
	}
	if !ok {
		return Result{Name: name, OK: true, Detail: "not registered (clio install-mcp)"}
	}
	if p := binaryProblem(cmd); p != "" {
		return Result{Name: name, OK: false, Detail: "registered command: " + p + "; rerun clio install-mcp"}
	}
	return Result{Name: name, OK: true, Detail: "registered: " + cmd}
}

// checkLease distinguishes "no mcp running" from a leader that died and left a
// stale lease behind (the next mcp takes over, but the index stops updating until
// one starts).
func checkLease(lockPath string) Result {
	const name = "mcp lease"
	st, err := lock.Inspect(lockPath)
	switch {
	case err != nil:
		return Result{Name: name, OK: false, Detail: "cannot read lease: " + err.Error()}
	case !st.Present:
		return Result{Name: name, OK: true, Detail: "no mcp running"}
	case st.Malformed:
		return Result{Name: name, OK: false, Detail: "lease file unreadable: " + lockPath}
	case st.Fresh:
		return Result{Name: name, OK: true, Detail: fmt.Sprintf("mcp leader pid %d", st.PID)}
	default:
		return Result{Name: name, OK: false, Detail: fmt.Sprintf("stale lease (last heartbeat %ds ago)", int64(st.HeartbeatAge/time.Second))}
	}
}

// checkIndexFreshness warns when nothing has been ingested for a day. MAX over all
// sources: a quiet source must not make a live index look stale.
func checkIndexFreshness(database *db.DB, now time.Time) Result {
	const name = "index freshness"
	var last sql.NullInt64
	if err := database.QueryRow(`SELECT MAX(last_ingested_at) FROM ingest_state`).Scan(&last); err != nil {
		return Result{Name: name, OK: false, Detail: err.Error()}
	}
	if !last.Valid {
		return Result{Name: name, OK: false, Detail: "never indexed; run clio index"}
	}
	age := now.Sub(time.Unix(last.Int64, 0))
	if age > staleIndexAge {
		return Result{Name: name, OK: false, Detail: fmt.Sprintf("index last updated %s ago; run clio index", humanAge(age))}
	}
	return Result{Name: name, OK: true, Detail: fmt.Sprintf("last updated %s ago", humanAge(age))}
}

func humanAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}
