package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/linhn0617/clio/internal/claudeconfig"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/lock"
)

// staleIndexAge is how far behind its source files the index may be before doctor
// warns. The MCP server normally keeps the index within minutes; a day of unabsorbed
// source changes means nothing is feeding it.
const staleIndexAge = 24 * time.Hour

// binaryProblem returns "" when path names an existing executable, otherwise a short
// reason. A bare name (no separator) is looked up on PATH the way a shell would.
func binaryProblem(path string) string { return binaryProblemOS(path, runtime.GOOS) }

// binaryProblemOS is binaryProblem for a given GOOS: Windows has no exec bit, so
// there an existing regular file is enough.
func binaryProblemOS(path, goos string) string {
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
	if fi.IsDir() || (goos != "windows" && fi.Mode().Perm()&0o111 == 0) {
		return "binary not executable: " + path
	}
	return ""
}

// checkHookBinary verifies the binaries clio's recall and file-history hooks point
// at. A reinstall that moves or deletes the binary leaves settings.json pointing at
// nothing, and the hook then fails silently on every session. Binary paths come
// from claudeconfig's recogniser; a relative one is flagged rather than resolved,
// because Claude Code runs hooks from the session's cwd, not from anywhere doctor
// could guess.
func checkHookBinary(settingsPath string) Result {
	const name = "hook binary"
	recall, fh, err := claudeconfig.ClioHookCommands(settingsPath)
	if err != nil {
		return Result{Name: name, OK: false, Detail: "cannot read " + settingsPath + ": " + err.Error()}
	}
	bins := append(append([]string{}, recall...), fh...)
	if len(bins) == 0 {
		return Result{Name: name, OK: true, Detail: "not installed (clio install-hook)"}
	}
	var problems, good []string
	seen := map[string]bool{}
	add := func(list *[]string, s string) {
		if !seen[s] {
			seen[s] = true
			*list = append(*list, s)
		}
	}
	for _, bin := range bins {
		switch {
		case !filepath.IsAbs(bin):
			add(&problems, "hook command is not an absolute path")
		case binaryProblem(bin) != "":
			add(&problems, "hook points to "+binaryProblem(bin))
		default:
			add(&good, bin)
		}
	}
	if len(problems) > 0 {
		return Result{Name: name, OK: false, Detail: strings.Join(problems, "; ") + "; rerun clio install-hook"}
	}
	return Result{Name: name, OK: true, Detail: strings.Join(good, ", ")}
}

// checkMCPRegistration reports whether clio is registered as an MCP server and, if
// so, whether its command still exists. Not registered is fine: MCP is optional.
func checkMCPRegistration(configPath string) Result {
	const name = "mcp registration"
	cmd, kind, err := claudeconfig.LookupServer(configPath, "clio")
	if err != nil {
		return Result{Name: name, OK: false, Detail: "cannot read " + configPath + ": " + err.Error()}
	}
	switch kind {
	case claudeconfig.ServerAbsent:
		return Result{Name: name, OK: true, Detail: "not registered (clio install-mcp)"}
	case claudeconfig.ServerRemote:
		return Result{Name: name, OK: true, Detail: "registered (url-based, no local binary to check)"}
	case claudeconfig.ServerMalformed:
		return Result{Name: name, OK: false, Detail: "malformed mcpServers.clio entry; rerun clio install-mcp"}
	}
	if p := binaryProblem(cmd); p != "" {
		return Result{Name: name, OK: false, Detail: "registered command: " + p + "; rerun clio install-mcp"}
	}
	return Result{Name: name, OK: true, Detail: "registered: " + cmd}
}

// checkLease tells apart: no mcp running; a healthy leader; a live leader whose
// heartbeat is late (busy or catching up, not dead); and a lease left by a process
// that no longer exists (the next mcp takes over, but the index stops updating until
// one starts).
func checkLease(lockPath string) Result {
	const name = "mcp lease"
	st, err := lock.Inspect(lockPath)
	secs := int64(st.HeartbeatAge / time.Second)
	switch {
	case err != nil:
		return Result{Name: name, OK: false, Detail: "cannot read lease: " + err.Error()}
	case !st.Present:
		return Result{Name: name, OK: true, Detail: "no mcp running"}
	case st.Malformed:
		return Result{Name: name, OK: false, Detail: "lease file unreadable: " + lockPath}
	case st.Fresh:
		return Result{Name: name, OK: true, Detail: fmt.Sprintf("mcp leader pid %d", st.PID)}
	case st.Alive:
		return Result{Name: name, OK: false, Detail: fmt.Sprintf("heartbeat %ds ago, pid %d alive (leader may be catching up)", secs, st.PID)}
	default:
		return Result{Name: name, OK: false, Detail: fmt.Sprintf("stale lease from pid %d (dead); restart Claude Code or remove %s", st.PID, lockPath)}
	}
}

// checkIndexFreshness measures whether the index is keeping up with its sources, not
// when it last ingested: last_ingested_at only moves when something is actually
// re-ingested, so a day without using Claude Code would warn forever and
// `clio index` could never clear it. A tracked file counts as behind when it holds
// bytes (or a rewrite) the index has not absorbed — the same conditions reconcile
// uses — and the worst such file's age is compared with staleIndexAge.
func checkIndexFreshness(database *db.DB, now time.Time) Result {
	const name = "index freshness"
	rows, err := database.Query(`SELECT source_file, last_size, last_mtime, last_byte_offset, ` + missingMarkExpr(database) + ` FROM ingest_state`)
	if err != nil {
		return Result{Name: name, OK: false, Detail: err.Error()}
	}
	defer rows.Close()
	var tracked int
	var worst time.Duration
	for rows.Next() {
		var path string
		var lastSize, lastMtime, offset int64
		var missingAt *int64
		if err := rows.Scan(&path, &lastSize, &lastMtime, &offset, &missingAt); err != nil {
			return Result{Name: name, OK: false, Detail: err.Error()}
		}
		tracked++
		fi, serr := os.Stat(path)
		if serr != nil || missingAt != nil {
			continue // reconciliation owns missing and retained files
		}
		unabsorbed := fi.Size() > offset || fi.Size() < lastSize ||
			(fi.Size() == lastSize && fi.ModTime().UnixNano() != lastMtime)
		if unabsorbed {
			if age := now.Sub(fi.ModTime()); age > worst {
				worst = age
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Result{Name: name, OK: false, Detail: err.Error()}
	}
	if tracked == 0 {
		return Result{Name: name, OK: false, Detail: "never indexed; run clio index"}
	}
	if worst > staleIndexAge {
		return Result{Name: name, OK: false, Detail: fmt.Sprintf("index behind source by %s; run clio index", humanAge(worst))}
	}
	var last int64
	if err := database.QueryRow(`SELECT COALESCE(MAX(last_ingested_at),0) FROM ingest_state`).Scan(&last); err != nil {
		return Result{Name: name, OK: false, Detail: err.Error()}
	}
	return Result{Name: name, OK: true, Detail: fmt.Sprintf("up to date (last change %s ago)", humanAge(now.Sub(time.Unix(last, 0))))}
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
