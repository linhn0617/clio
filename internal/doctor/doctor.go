// Package doctor runs health checks over the index, including structural checks
// (DB integrity, FTS sync) and semantic source-of-truth reconciliation that a
// plain integrity check cannot catch (truncated or missing source files).
package doctor

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
	sqlitelib "modernc.org/sqlite"
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

	// The Codex source root is optional: report it only when present, and treat its
	// absence as "not installed", never a failure. Resolved up front because whether
	// it's present changes how a missing Claude projects dir is judged below.
	codexDir, codexDirErr := config.CodexSessionsDir()
	codexPresent := false
	if codexDirErr == nil {
		if _, serr := os.Stat(codexDir); serr == nil {
			codexPresent = true
		}
	}

	claudePresent := false
	if _, err := os.Stat(projectsDir); err == nil {
		claudePresent = true
	}
	if ok, note := claudeDirStatus(claudePresent, codexPresent); !ok {
		add("claude projects dir", false, fmt.Sprintf("not found: %s", projectsDir))
	} else if claudePresent {
		add("claude projects dir", true, projectsDir)
		availRoots = append(availRoots, projectsDir)
	} else {
		// Missing, but a supported configuration (e.g. Codex-only install): still
		// reported so the absence is visible, but not as a warning.
		add("claude projects dir", true, fmt.Sprintf("not found (%s): %s", note, projectsDir))
	}
	if codexPresent {
		add("codex sessions dir", true, codexDir)
		availRoots = append(availRoots, codexDir)
	}

	// DB integrity.
	var integ string
	if err := database.QueryRow(`PRAGMA integrity_check`).Scan(&integ); err != nil {
		add("db integrity", false, err.Error())
	} else {
		add("db integrity", integ == "ok", integ)
	}

	// FTS sync: messages vs fts row counts, then (only when counts match) a
	// content-level integrity check. Row counts alone can't see the index
	// desynced from message content while row bookkeeping stays consistent
	// (see ftsContentIntegrityOK) — a real defect a row-count-only check
	// cannot distinguish from a healthy index.
	var msgCount, ftsCount int
	msgErr := database.QueryRow(`SELECT count(*) FROM messages`).Scan(&msgCount)
	ftsErr := database.QueryRow(`SELECT count(*) FROM messages_fts`).Scan(&ftsCount)
	switch {
	case msgErr != nil || ftsErr != nil:
		e := msgErr
		if e == nil {
			e = ftsErr
		}
		add("fts index", false, e.Error())
	case msgCount != ftsCount:
		add("fts index", false, fmt.Sprintf("%d messages / %d fts rows", msgCount, ftsCount))
	default:
		if ok, detail, ierr := ftsContentIntegrityOK(dbPath); ierr != nil {
			add("fts index", false, fmt.Sprintf("%d messages / %d fts rows, content check failed: %s", msgCount, ftsCount, ierr.Error()))
		} else if !ok {
			add("fts index", false, fmt.Sprintf("%d messages / %d fts rows, %s", msgCount, ftsCount, detail))
		} else {
			add("fts index", true, fmt.Sprintf("%d messages / %d fts rows, content verified", msgCount, ftsCount))
		}
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
	missing, preserved, truncated, rewritten, lag, rerr := reconcile(database, availRoots)
	if rerr != nil {
		add("source reconciliation", false, rerr.Error())
	} else {
		detail := fmt.Sprintf("%d missing/unreadable files, %d truncated, %d same-size rewrite(s) needing re-ingest, %d with new unindexed bytes",
			missing, truncated, rewritten, lag)
		if preserved > 0 {
			detail += fmt.Sprintf(", %d preserved (source root unavailable)", preserved)
		}
		// Every category above is a real discrepancy between the index and its source
		// files: none of them may be silently treated as healthy, or doctor would
		// report OK/exit 0 while the detail text itself lists live problems.
		add("source reconciliation", missing == 0 && truncated == 0 && rewritten == 0 && lag == 0, detail)
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

	// Ingest coverage: files on disk vs files in ingest_state, partitioned per source
	// root so a well-covered source (e.g. Codex, with many tracked rows) cannot mask a
	// coverage gap in another (e.g. Claude Code files never ingested).
	if len(availRoots) > 0 {
		if ok, detail, cerr := coverageBySource(database, availRoots); cerr != nil {
			add("ingest coverage", false, cerr.Error())
		} else {
			add("ingest coverage", ok, detail)
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

// ftsContentIntegrityOK runs SQLite FTS5's external-content integrity check
// (`INSERT INTO messages_fts(messages_fts, rank) VALUES('integrity-check', 1)`)
// against the index at dbPath. Unlike a row-count comparison, this walks the
// trigram postings and verifies them against messages.content, so it catches
// index/content divergence that leaves row counts untouched (verified against
// a real sqlite3 CLI repro in scratchpad: deleting a row's fts entry keyed on
// the wrong old content, rather than through the AFTER UPDATE/DELETE
// triggers, leaves messages_fts's row count unchanged but its postings
// inconsistent — plain `integrity-check` with no rank argument does NOT catch
// this, only the rank=1 form does).
//
// This requires a writable connection: SQLite implements FTS5 maintenance
// commands as an INSERT against the virtual table, which a mode=ro connection
// refuses outright ("attempt to write a readonly database"), confirmed
// against the same repro. doctor's CLI caller opens the DB read-only (see
// internal/cli/doctor.go) so callers aren't forced to hold the whole
// diagnostic run on a writable handle just for this one check; instead this
// opens its own short-lived writable connection to dbPath and closes it
// immediately after. The command does not persist any change to the database
// file (verified: no WAL/SHM sidecar appears and the file's size is
// unchanged after running it), so this is safe to run against a live index.
//
// This does not run schema migrations (unlike db.Open): it is a single
// INSERT statement against an already-migrated messages_fts table. On a
// pre-migration DB missing that table, or when the DB can't be opened
// writable at all (e.g. permission denied, or contention that outlasts the
// busy_timeout), the query error is returned via err for the caller to
// report like any other check failure — it is not conflated with a genuine
// integrity failure.
func ftsContentIntegrityOK(dbPath string) (ok bool, detail string, err error) {
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(3000)&mode=rw"
	conn, oerr := sql.Open("sqlite", dsn)
	if oerr != nil {
		return false, "", fmt.Errorf("open writable connection for fts integrity-check: %w", oerr)
	}
	defer conn.Close()

	_, cerr := conn.Exec(`INSERT INTO messages_fts(messages_fts, rank) VALUES('integrity-check', 1)`)
	if cerr == nil {
		return true, "", nil
	}
	var se *sqlitelib.Error
	if errors.As(cerr, &se) && se.Code()&0xff == 11 {
		// 11 = SQLITE_CORRUPT primary result code, masking off any extended-code
		// bits (e.g. SQLITE_CORRUPT_VTAB reports as 267 = 11 | (1<<8)) — mirrors
		// the SQLITE_BUSY/SQLITE_LOCKED check in internal/db/db.go's isBusyErr.
		return false, "trigram index content mismatch (fts5 integrity-check failed): " + cerr.Error(), nil
	}
	return false, "", fmt.Errorf("fts5 integrity-check: %w", cerr)
}

func reconcile(database *db.DB, availRoots []string) (missing, preserved, truncated, rewritten, lag int, err error) {
	rows, qerr := database.Query(`SELECT source_file, last_size, last_mtime, last_byte_offset FROM ingest_state`)
	if qerr != nil {
		return 0, 0, 0, 0, 0, qerr
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		var lastSize, lastMtime, offset int64
		if serr := rows.Scan(&path, &lastSize, &lastMtime, &offset); serr != nil {
			return 0, 0, 0, 0, 0, serr
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
		case fi.Size() == lastSize && fi.ModTime().UnixNano() != lastMtime:
			// Same size, different mtime: ingest's own classifyChange
			// (internal/ingest/incremental.go) treats this as a full rewrite, never
			// an append — a same-size content swap that a size-only check can't see.
			rewritten++
		case fi.Size() > offset:
			lag++
		}
	}
	return missing, preserved, truncated, rewritten, lag, rows.Err()
}

// coverageBySource compares on-disk session files to tracked ingest_state rows,
// partitioned per source root, so a source with heavy coverage (e.g. many tracked
// Codex rows) cannot mask a coverage gap in a different source (e.g. Claude Code files
// never ingested). ingest_state has no source column, so membership is decided by path
// prefix against each root, mirroring reconcile's underAnyDir.
func coverageBySource(database *db.DB, roots []string) (ok bool, detail string, err error) {
	rows, qerr := database.Query(`SELECT source_file FROM ingest_state`)
	if qerr != nil {
		return false, "", qerr
	}
	defer rows.Close()
	var tracked []string
	for rows.Next() {
		var path string
		if serr := rows.Scan(&path); serr != nil {
			return false, "", serr
		}
		tracked = append(tracked, path)
	}
	if rerr := rows.Err(); rerr != nil {
		return false, "", rerr
	}

	ok = true
	var parts []string
	for _, root := range roots {
		files, werr := ingest.WalkSessionFiles(root, nil)
		if werr != nil {
			ok = false
			parts = append(parts, fmt.Sprintf("%s: walk error: %s", filepath.Base(root), werr.Error()))
			continue
		}
		var n int
		for _, p := range tracked {
			if underAnyDir(p, []string{root}) {
				n++
			}
		}
		if len(files) > n {
			ok = false
		}
		parts = append(parts, fmt.Sprintf("%s: %d on disk, %d tracked", filepath.Base(root), len(files), n))
	}
	return ok, strings.Join(parts, "; "), nil
}

// claudeDirStatus judges the ~/.claude/projects presence check. Its absence is only a
// genuine problem when no other source is available: a Codex-only install (no Claude
// Code projects dir, Codex present) is a supported configuration and must not warn.
// note explains a non-warning absence for the caller's detail text; empty when present
// or when the absence is a real failure.
func claudeDirStatus(present, codexPresent bool) (ok bool, note string) {
	if present {
		return true, ""
	}
	if codexPresent {
		return true, "codex-only install, supported"
	}
	return false, ""
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
