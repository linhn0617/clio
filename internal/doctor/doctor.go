// Package doctor runs health checks over the index, including structural checks
// (DB integrity, FTS sync) and semantic source-of-truth reconciliation that a
// plain integrity check cannot catch (truncated or missing source files).
package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
)

// Result is one check outcome.
type Result struct {
	Name   string
	OK     bool
	Detail string
}

// Run executes all checks and returns their results.
func Run(database *db.DB, projectsDir, dbPath string) []Result {
	var out []Result
	add := func(name string, ok bool, detail string) {
		out = append(out, Result{name, ok, detail})
	}

	var availRoots []string
	if _, err := os.Stat(projectsDir); err != nil {
		add("claude projects dir", false, fmt.Sprintf("not found: %s", projectsDir))
	} else {
		add("claude projects dir", true, projectsDir)
		availRoots = append(availRoots, projectsDir)
	}
	// The Codex source root is optional: report it only when present, and treat its
	// absence as "not installed", never a failure.
	if codexDir, err := config.CodexSessionsDir(); err == nil {
		if _, serr := os.Stat(codexDir); serr == nil {
			add("codex sessions dir", true, codexDir)
			availRoots = append(availRoots, codexDir)
		}
	}

	// DB integrity.
	var integ string
	if err := database.QueryRow(`PRAGMA integrity_check`).Scan(&integ); err != nil {
		add("db integrity", false, err.Error())
	} else {
		add("db integrity", integ == "ok", integ)
	}

	// FTS sync: messages vs fts row counts.
	var msgCount, ftsCount int
	msgErr := database.QueryRow(`SELECT count(*) FROM messages`).Scan(&msgCount)
	ftsErr := database.QueryRow(`SELECT count(*) FROM messages_fts`).Scan(&ftsCount)
	if msgErr != nil || ftsErr != nil {
		e := msgErr
		if e == nil {
			e = ftsErr
		}
		add("fts index", false, e.Error())
	} else {
		add("fts index", msgCount == ftsCount, fmt.Sprintf("%d messages / %d fts rows", msgCount, ftsCount))
	}

	// Orphan sessions (no messages).
	var orphans int
	if err := database.QueryRow(`SELECT count(*) FROM sessions s WHERE NOT EXISTS (SELECT 1 FROM messages m WHERE m.session_uuid = s.uuid)`).Scan(&orphans); err != nil {
		add("orphan sessions", false, err.Error())
	} else {
		add("orphan sessions", orphans == 0, fmt.Sprintf("%d sessions with no messages", orphans))
	}

	// Source-of-truth reconciliation: compare ingest_state against the files. Files
	// under a currently-unavailable source root are "preserved" (kept by design),
	// reported separately from genuinely missing files so a stale index isn't read
	// as healthy and an unmounted root isn't read as deletions.
	missing, preserved, truncated, lag, rerr := reconcile(database, availRoots)
	if rerr != nil {
		add("source reconciliation", false, rerr.Error())
	} else {
		detail := fmt.Sprintf("%d missing/unreadable files, %d truncated, %d with new unindexed bytes", missing, truncated, lag)
		if preserved > 0 {
			detail += fmt.Sprintf(", %d preserved (source root unavailable)", preserved)
		}
		add("source reconciliation", missing == 0 && truncated == 0, detail)
	}

	// Cross-source uuid conflicts: files refused at ingest because their uuid is
	// already owned by a different source (recorded durably in source_conflicts).
	var conflicts int
	cerr := database.QueryRow(`SELECT count(*) FROM source_conflicts`).Scan(&conflicts)
	switch {
	case cerr != nil && strings.Contains(cerr.Error(), "no such table"):
		add("source conflicts", true, "0 (pre-migration db)")
	case cerr != nil:
		add("source conflicts", false, cerr.Error())
	case conflicts == 0:
		add("source conflicts", true, "0")
	default:
		add("source conflicts", false, fmt.Sprintf("%d file(s) unindexed due to a cross-source uuid conflict", conflicts))
	}

	// Unparsed lines: complete source lines ingest could not parse (recorded per source).
	// doctor opens the DB read-only, so on a pre-0005 DB the column may not exist yet
	// (migrations run on the next writable open) — treat that as legacy 0, not a failure.
	var unparsed int64
	uerr := database.QueryRow(`SELECT COALESCE(SUM(unparsed_lines),0) FROM ingest_state`).Scan(&unparsed)
	switch {
	case uerr != nil && strings.Contains(uerr.Error(), "no such column"):
		add("unparsed lines", true, "0 (pre-migration db)")
	case uerr != nil:
		add("unparsed lines", false, uerr.Error())
	case unparsed == 0:
		add("unparsed lines", true, "0")
	default:
		add("unparsed lines", false, fmt.Sprintf("%d source lines could not be parsed; after upgrading clio, run `clio index --full`", unparsed))
	}

	// File permissions: the DB and its WAL/SHM sidecars hold indexed content and must
	// be private (0600). Absent sidecars (no writes yet) are skipped.
	var badPerms []string
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		fi, e := os.Stat(p)
		if errors.Is(e, fs.ErrNotExist) {
			continue // sidecar not created yet
		}
		if e != nil {
			badPerms = append(badPerms, filepath.Base(p)+"=unverifiable("+e.Error()+")")
			continue
		}
		if mode := fi.Mode().Perm(); mode != 0o600 {
			badPerms = append(badPerms, fmt.Sprintf("%s=%04o", filepath.Base(p), mode))
		}
	}
	if len(badPerms) == 0 {
		add("file permissions", true, "0600")
	} else {
		add("file permissions", false, "not 0600: "+strings.Join(badPerms, ", "))
	}

	// Ingest coverage: files on disk vs files in ingest_state.
	if files, err := ingest.WalkSessionFiles(projectsDir, nil); err == nil {
		var tracked int
		if serr := database.QueryRow(`SELECT count(*) FROM ingest_state`).Scan(&tracked); serr != nil {
			add("ingest coverage", false, serr.Error())
		} else {
			add("ingest coverage", len(files) <= tracked, fmt.Sprintf("%d files on disk, %d tracked", len(files), tracked))
		}
	}

	// DB size warning vs source size.
	if fi, err := os.Stat(dbPath); err == nil {
		srcBytes, serr := sourceBytes(database)
		if serr != nil {
			add("db size", false, serr.Error())
		} else {
			ratio := 0.0
			if srcBytes > 0 {
				ratio = float64(fi.Size()) / float64(srcBytes)
			}
			ok := ratio < 3.0 || srcBytes == 0
			add("db size", ok, fmt.Sprintf("db %.1f MB, ~%.1fx source", float64(fi.Size())/1e6, ratio))
		}
	}

	return out
}

func reconcile(database *db.DB, availRoots []string) (missing, preserved, truncated, lag int, err error) {
	rows, qerr := database.Query(`SELECT source_file, last_size, last_byte_offset FROM ingest_state`)
	if qerr != nil {
		return 0, 0, 0, 0, qerr
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		var lastSize, offset int64
		if serr := rows.Scan(&path, &lastSize, &offset); serr != nil {
			return 0, 0, 0, 0, serr
		}
		fi, statErr := os.Stat(path)
		if statErr != nil {
			// A file whose source root is currently unavailable is preserved by
			// design (PurgeMissing keeps it), not a deletion — count it separately.
			// Otherwise it is genuinely missing/unverifiable; flag it rather than
			// silently skipping, so the check does not false-green.
			if !underAnyDir(path, availRoots) {
				preserved++
			} else {
				missing++
			}
			continue
		}
		switch {
		case fi.Size() < lastSize:
			truncated++
		case fi.Size() > offset:
			lag++
		}
	}
	return missing, preserved, truncated, lag, rows.Err()
}

// underAnyDir reports whether path lies within any of roots.
func underAnyDir(path string, roots []string) bool {
	for _, r := range roots {
		if rel, err := filepath.Rel(r, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func sourceBytes(database *db.DB) (int64, error) {
	var total int64
	err := database.QueryRow(`SELECT COALESCE(SUM(last_size),0) FROM ingest_state`).Scan(&total)
	return total, err
}
