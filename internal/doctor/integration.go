package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/linhn0617/clio/internal/claudeconfig"
	"github.com/linhn0617/clio/internal/lock"
)

// staleIndexAge is how old the oldest unindexed source change may be before doctor
// warns. The MCP server normally keeps the index within minutes; a day of unabsorbed
// source changes means nothing is feeding it.
const staleIndexAge = 24 * time.Hour

// binaryProblem returns "" when the absolute path names an existing executable,
// otherwise a short reason. Callers handle non-absolute commands themselves: a hook
// or MCP command is resolved by Claude Code, not by doctor.
func binaryProblem(path string) string { return binaryProblemOS(path, runtime.GOOS) }

// binaryProblemOS is binaryProblem for a given GOOS: Windows has no exec bit, so
// there an existing regular file is enough.
func binaryProblemOS(path, goos string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return "missing binary " + path
	}
	if fi.IsDir() || (goos != "windows" && fi.Mode().Perm()&0o111 == 0) {
		return "binary not executable: " + path
	}
	return ""
}

// checkHookBinary verifies clio's recall and file-history hooks, each on its own.
// A reinstall that moves or deletes the binary leaves settings.json pointing at
// nothing, and the hook then fails silently on every session; an upgrade without
// rerunning install-hook leaves one hook missing or file-history under the older
// "Read" matcher. Binary paths come from claudeconfig's recogniser; a relative one
// is flagged rather than resolved, because Claude Code runs hooks from the
// session's cwd, not from anywhere doctor could guess.
func checkHookBinary(settingsPath string) Result {
	const name = "hook binary"
	recall, fh, err := claudeconfig.ClioHookCommands(settingsPath)
	if err != nil {
		return Result{Name: name, OK: false, Detail: "cannot read " + settingsPath + ": " + err.Error()}
	}
	if len(recall) == 0 && len(fh) == 0 {
		return Result{Name: name, OK: true, Detail: "not installed (clio install-hook)"}
	}
	var problems, good, notes []string
	seen := map[string]bool{}
	problem := func(s string) {
		if !seen[s] {
			seen[s] = true
			problems = append(problems, s)
		}
	}
	for _, hook := range []struct {
		kind    string
		entries []claudeconfig.HookEntry
	}{{"recall", recall}, {"file-history", fh}} {
		if len(hook.entries) == 0 {
			problem(hook.kind + " hook not installed")
			continue
		}
		var bins []string
		for _, e := range hook.entries {
			switch {
			case e.Unquoted:
				problem(hook.kind + " hook has an unquoted path with whitespace")
			case !filepath.IsAbs(e.Binary):
				problem(hook.kind + " hook command is not an absolute path")
			default:
				if p := binaryProblem(e.Binary); p != "" {
					problem(hook.kind + " hook points to " + p)
				} else if !slices.Contains(bins, e.Binary) {
					bins = append(bins, e.Binary)
				}
			}
			if e.OldMatcher {
				problem(hook.kind + " hook matcher is Read, needs upgrade")
			}
			// install-hook leaves the user's own placement alone, so this is a note.
			if note := hook.kind + " hook matcher " + e.Uncovered + " does not cover Read|Bash"; e.Uncovered != "" && !slices.Contains(notes, note) {
				notes = append(notes, note)
			}
		}
		good = append(good, hook.kind+": "+strings.Join(bins, ", "))
	}
	if len(problems) > 0 {
		return Result{Name: name, OK: false, Detail: strings.Join(problems, "; ") + "; rerun clio install-hook"}
	}
	return Result{Name: name, OK: true, Detail: strings.Join(append(good, notes...), "; ")}
}

// checkMCPRegistration reports whether clio is registered as an MCP server and, if
// so, whether its command still exists. Not registered is fine: MCP is optional.
// install-mcp falls back to a bare `clio` when it cannot resolve its own path, and
// Claude Code then looks that up on its own PATH, which doctor cannot see; so a
// non-absolute command passes with a note instead of being judged on doctor's PATH
// (or, for a relative path with a separator, on doctor's working directory).
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
	if !filepath.IsAbs(cmd) && strings.ContainsAny(cmd, "/"+string(filepath.Separator)) {
		// Not a PATH lookup at all, and doctor's cwd says nothing about Claude Code's.
		return Result{Name: name, OK: true, Detail: "registered: " + cmd + " (relative path, resolved against Claude Code's working directory, not doctor's; prefer an absolute path)"}
	}
	if !filepath.IsAbs(cmd) {
		const note = "resolved on doctor's PATH, not Claude Code's; prefer an absolute path"
		if found, lerr := exec.LookPath(cmd); lerr == nil {
			return Result{Name: name, OK: true, Detail: "registered: " + cmd + " -> " + found + " (" + note + ")"}
		}
		return Result{Name: name, OK: true, Detail: "registered: " + cmd + " (not found on doctor's PATH, which is not Claude Code's; prefer an absolute path)"}
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

// checkIndexFreshness judges whether the index keeps up with its sources, from the
// numbers source reconciliation already gathered: it walks nothing itself, so the two
// lines cannot disagree. It warns when the oldest unindexed change (a truncation, a
// rewrite, or a complete unindexed line) is older than staleIndexAge. Last ingest
// time says nothing here: a day without using Claude Code is not a fault. A partial
// last line (a session that stopped mid-write) is not a change, since ingest only
// absorbs complete lines and `clio index` could never clear it.
func checkIndexFreshness(rec reconcileStats, rerr error) Result {
	const name = "index freshness"
	switch {
	case rerr != nil:
		return Result{Name: name, OK: false, Detail: "unavailable (source reconciliation failed): " + rerr.Error()}
	case rec.tracked == 0:
		return Result{Name: name, OK: false, Detail: "never indexed; run clio index"}
	case rec.oldestChange > staleIndexAge:
		return Result{Name: name, OK: false, Detail: fmt.Sprintf("oldest unindexed change %s old; run clio index", humanAge(rec.oldestChange))}
	}
	detail := "no unindexed changes"
	if rec.oldestChange > 0 {
		detail = fmt.Sprintf("oldest unindexed change %s old (warns after %s)", humanAge(rec.oldestChange), humanAge(staleIndexAge))
	}
	if rec.partial > 0 {
		detail += fmt.Sprintf(" (%d file(s) end in a partial line, indexed once complete)", rec.partial)
	}
	return Result{Name: name, OK: true, Detail: detail}
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
