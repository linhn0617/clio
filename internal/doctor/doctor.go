// Package doctor runs health checks over the index, including structural checks
// (DB integrity, FTS sync) and semantic source-of-truth reconciliation that a
// plain integrity check cannot catch (truncated or missing source files).
package doctor

import (
	"fmt"
	"os"

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

	if _, err := os.Stat(projectsDir); err != nil {
		add("claude projects dir", false, fmt.Sprintf("not found: %s", projectsDir))
	} else {
		add("claude projects dir", true, projectsDir)
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

	// Source-of-truth reconciliation: compare ingest_state against the files.
	missing, truncated, lag, rerr := reconcile(database)
	if rerr != nil {
		add("source reconciliation", false, rerr.Error())
	} else {
		add("source reconciliation", missing == 0 && truncated == 0, fmt.Sprintf("%d missing files, %d truncated, %d with new unindexed bytes", missing, truncated, lag))
	}

	// Ingest coverage: files on disk vs files in ingest_state.
	if files, err := ingest.WalkSessionFiles(projectsDir); err == nil {
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

func reconcile(database *db.DB) (missing, truncated, lag int, err error) {
	rows, qerr := database.Query(`SELECT source_file, last_size, last_byte_offset FROM ingest_state`)
	if qerr != nil {
		return 0, 0, 0, qerr
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		var lastSize, offset int64
		if serr := rows.Scan(&path, &lastSize, &offset); serr != nil {
			return 0, 0, 0, serr
		}
		fi, statErr := os.Stat(path)
		if os.IsNotExist(statErr) {
			missing++
			continue
		}
		if statErr != nil {
			continue // transient FS error: don't fail the whole DB-health check
		}
		switch {
		case fi.Size() < lastSize:
			truncated++
		case fi.Size() > offset:
			lag++
		}
	}
	return missing, truncated, lag, rows.Err()
}

func sourceBytes(database *db.DB) (int64, error) {
	var total int64
	err := database.QueryRow(`SELECT COALESCE(SUM(last_size),0) FROM ingest_state`).Scan(&total)
	return total, err
}
