// Package ingest scans Claude Code session files and writes them into the DB.
package ingest

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/model"
)

// errStaleSnapshot means the source file changed between read and commit; the
// caller treats it as a no-op and lets a later pass re-ingest the fresh bytes.
var errStaleSnapshot = errors.New("source file changed during ingest")

// errSourceConflict means the session uuid is already owned by a different source
// tool; the file is refused (no rows written) and the collision is recorded in
// source_conflicts for diagnostics. The caller treats it as a per-file skip.
var errSourceConflict = errors.New("session uuid already owned by a different source")

// Ingester writes parsed sessions into the database.
type Ingester struct {
	db  *db.DB
	log *slog.Logger
	// sources is the registry of ingestion adapters, consulted in order; the
	// claude-code fallback is last. A file is routed to the first source that owns it.
	sources []Source
	// preCommitHook, if non-nil, runs inside commit() just after BEGIN and
	// before any writes. Tests use it to mutate the on-disk file and exercise
	// the re-validation guard. Always nil in production.
	preCommitHook func()
}

// New returns an Ingester with the claude-code source registered. If log is nil,
// logging is discarded.
func New(database *db.DB, log *slog.Logger) *Ingester {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Ingester{db: database, log: log, sources: []Source{claudeCodeSource{}}}
}

// AddSource registers an additional ingestion adapter, consulted before the
// claude-code fallback so its more specific Owns() wins.
func (ing *Ingester) AddSource(s Source) {
	ing.sources = append([]Source{s}, ing.sources...)
}

// Stats summarizes an ingest run.
type Stats struct {
	FilesScanned  int
	FilesIngested int
	FilesSkipped  int
	MessagesAdded int
}

// IngestAll walks projectsDir and ingests every .jsonl file. force re-ingests
// from scratch regardless of stored state. The context is checked before each
// file so a cancelled (demoted) leader stops promptly.
func (ing *Ingester) IngestAll(ctx context.Context, projectsDir string, force bool) (Stats, error) {
	files, err := WalkSessionFiles(projectsDir, ing.log)
	if err != nil {
		// Don't abort the whole run: other registered sources (e.g. Codex) may still
		// have files to ingest even when the Claude Code projects dir is unavailable.
		ing.log.Warn("walk claude projects dir failed; continuing with other sources", "dir", projectsDir, "err", err)
		files = nil
	}
	// Span every registered source: a missing/unavailable extra root (e.g. Codex not
	// installed) is logged inside extraRoots/WalkSessionFiles, never fatal.
	for _, root := range ing.extraRoots() {
		more, werr := WalkSessionFiles(root, ing.log)
		if werr != nil {
			ing.log.Warn("walk source root failed", "root", root, "err", werr)
			continue
		}
		files = append(files, more...)
	}
	var st Stats
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if seen[f] { // de-dup overlapping roots
			continue
		}
		seen[f] = true
		if err := ctx.Err(); err != nil {
			return st, err
		}
		st.FilesScanned++
		n, ingested, err := ing.IngestFile(ctx, f, force)
		if err != nil {
			ing.log.Warn("ingest file failed", "file", f, "err", err)
			continue
		}
		if ingested {
			st.FilesIngested++
			st.MessagesAdded += n
		} else {
			st.FilesSkipped++
		}
	}
	return st, nil
}

// IngestFile ingests a single .jsonl file. Returns the number of messages added
// and whether any ingest happened (false = skipped as unchanged). The whole
// file is committed in one transaction.
func (ing *Ingester) IngestFile(ctx context.Context, path string, force bool) (int, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	src := ing.sourceFor(path)
	if src == nil {
		return 0, false, nil // no source adapter owns this path
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false, err
	}
	size, mtime := fi.Size(), fi.ModTime().UnixNano()

	prior, err := ing.loadState(path)
	if err != nil {
		return 0, false, err
	}

	kind := classifyChange(prior, size, mtime)
	if force {
		kind = changeFull
	}
	if kind == changeSkip {
		return 0, false, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()

	startOffset := int64(0)
	if kind == changeIncremental && prior != nil {
		tailFP, err := fingerprintAt(f, prior.LastByteOffset)
		if err != nil {
			return 0, false, err
		}
		headFP, err := headFingerprint(f)
		if err != nil {
			return 0, false, err
		}
		// Empty stored head fingerprint = unknown (row predates migration 0005): skip
		// the head check and backfill it (eng-review D2), so upgrades don't force a full
		// reingest of every file. This branch is only reachable when the file GREW
		// (classifyChange returns changeIncremental only for size growth; a same-size
		// rewrite is changeFull), i.e. a genuine append. Claude session files are
		// append-only, so an append never rewrites the prefix — resuming from the stored
		// offset is safe. (A prefix rewrite on a grown file would need non-append
		// behavior these files never exhibit; that is the documented fingerprint-window
		// limitation, not specific to the empty-head case.)
		headOK := prior.HeadFingerprint == "" || headFP == prior.HeadFingerprint
		if tailFP == prior.TailFingerprint && headOK {
			startOffset = prior.LastByteOffset
		} else {
			kind = changeFull // bytes before the offset changed: rewritten
		}
	}

	startSeq := 0
	if kind == changeIncremental {
		startSeq, err = ing.maxSeq(src.SessionIDFromPath(path))
		if err != nil {
			return 0, false, err
		}
		startSeq++
	}

	res, err := src.ParseFile(ing, f, startOffset, startSeq, path)
	if err != nil {
		return 0, false, err
	}
	if res.Consumed == 0 {
		return 0, false, nil // no complete new line yet
	}
	newOffset := startOffset + res.Consumed

	newTailFP, err := fingerprintAt(f, newOffset)
	if err != nil {
		return 0, false, err
	}
	newHeadFP, err := headFingerprint(f)
	if err != nil {
		return 0, false, err
	}

	n, err := ing.commit(kind, res.Session, res.Messages, res.ClioIDs, FileState{
		SourceFile:      path,
		LastSize:        size,
		LastMTime:       mtime,
		LastByteOffset:  newOffset,
		TailFingerprint: newTailFP,
		HeadFingerprint: newHeadFP,
		LastIngestedAt:  time.Now().Unix(),
		UnparsedLines:   res.Unparsed,
	})
	if err != nil {
		if errors.Is(err, errStaleSnapshot) || errors.Is(err, errSourceConflict) {
			return 0, false, nil // changed under us, or refused as a cross-source conflict (recorded for doctor)
		}
		return 0, false, err
	}
	return n, true, nil
}

// streamParse reads complete lines from f starting at startOffset, parses each, and
// accumulates messages + session metadata with bounded per-line memory. It returns the
// bytes consumed (sum of complete lines including their newline; a trailing partial or
// over-cap-without-newline line is NOT consumed and is left for the next pass) and the
// count of lines skipped because they failed to parse or exceeded maxLineBytes.
func (ing *Ingester) streamParse(f *os.File, startOffset int64, p *Parser, path string) ([]model.Message, model.Session, int64, int64, error) {
	sess := model.Session{UUID: sessionUUIDFromPath(path), SourceFile: path, Source: model.SourceClaudeCode}
	isSub := isSubagentFile(path)
	var msgs []model.Message
	var consumed, unparsed int64

	if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
		return nil, sess, 0, 0, err
	}
	r := bufio.NewReader(f)
	for {
		data, n, terminated, overCap, err := readCappedLine(r, maxLineBytes)
		if err != nil && err != io.EOF {
			return nil, sess, 0, 0, err
		}
		if !terminated {
			break // partial trailing line (incl. over-cap w/o newline): leave for next pass
		}
		consumed += int64(n)
		if overCap {
			ing.log.Warn("skip over-cap line", "file", path, "bytes", n)
			unparsed++
			continue
		}
		line := bytes.TrimSuffix(data, []byte("\n"))
		if len(line) == 0 {
			continue
		}
		lineMsgs, info, perr := p.ParseLine(line)
		if perr != nil {
			ing.log.Warn("skip malformed line", "file", path, "err", perr)
			unparsed++
			continue
		}
		if sess.UUID == "" && info.SessionID != "" {
			sess.UUID = info.SessionID
		}
		if sess.ProjectPath == "" && info.CWD != "" {
			sess.ProjectPath = info.CWD
		}
		if sess.Title == "" && info.TitleHint != "" {
			sess.Title = info.TitleHint
		}
		if isSub {
			// A subagent transcript carries its parent session's uuid in each line's
			// sessionId; record it as the parent link (the child keeps its agent-<id>
			// filename uuid). attributionAgent is the subagent's type.
			if sess.ParentSession == "" && info.SessionID != "" {
				sess.ParentSession = info.SessionID
			}
			if sess.AgentType == "" && info.AgentType != "" {
				sess.AgentType = info.AgentType
			}
		}
		if info.TS != 0 {
			if sess.StartedAt == 0 || info.TS < sess.StartedAt {
				sess.StartedAt = info.TS
			}
			if info.TS > sess.EndedAt {
				sess.EndedAt = info.TS
			}
		}
		for i := range lineMsgs {
			lineMsgs[i].SessionUUID = sess.UUID // canonical: filename uuid
		}
		msgs = append(msgs, lineMsgs...)
	}
	if sess.ProjectPath == "" {
		dir := parentDirName(path)
		if isSub {
			dir = subagentProjectDirName(path) // skip the <parent>/subagents/ levels
		}
		sess.ProjectPath = fallbackProjectPath(dir)
	}
	if isSub && sess.ParentSession == "" {
		sess.ParentSession = subagentParentDir(path)
	}
	return msgs, sess, consumed, unparsed, nil
}

// readCappedLine reads one line (through '\n') from r with bounded memory. It returns
// the line bytes (including the trailing '\n' when terminated; nil when over-cap), the
// bytes consumed from r, whether a '\n' was found (terminated), whether the line
// exceeded limit (overCap), and any read error (io.EOF at stream end). A line longer
// than limit is consumed but not buffered, so a giant or newline-less line cannot OOM.
func readCappedLine(r *bufio.Reader, limit int) (data []byte, consumed int, terminated, overCap bool, err error) {
	for {
		chunk, e := r.ReadSlice('\n')
		consumed += len(chunk)
		if !overCap {
			if len(data)+len(chunk) <= limit {
				data = append(data, chunk...)
			} else {
				overCap = true
				data = nil // stop buffering; only the byte count matters from here
			}
		}
		switch e {
		case nil:
			return data, consumed, true, overCap, nil
		case bufio.ErrBufferFull:
			continue
		default: // io.EOF or a real read error
			return data, consumed, false, overCap, e
		}
	}
}

// checkSourceConflict reports whether sess.UUID is already owned by a different
// source. On conflict it records the collision durably in source_conflicts and
// returns true (the caller must refuse the ingest). With no conflict it clears any
// stale conflict row for this file. It runs before the commit transaction opens, so
// a refused file never writes session/message rows under another tool's uuid. A
// NULL/empty stored source is treated as claude-code (rows predating migration 0009).
// checkSourceConflict runs inside commit's IMMEDIATE transaction (atomic vs racing
// writers): it reports whether sess.UUID is already owned by a different source. On
// conflict it records the collision durably in source_conflicts via tx and returns
// true (the caller commits that record and refuses the file); with no conflict it
// clears any stale conflict row for this file. A NULL/empty stored source is treated
// as claude-code (rows predating migration 0009).
func (ing *Ingester) checkSourceConflict(tx *sql.Tx, uuid, source, sourceFile string) (bool, error) {
	var existing sql.NullString
	err := tx.QueryRow(`SELECT source FROM sessions WHERE uuid = ?`, uuid).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		_, derr := tx.Exec(`DELETE FROM source_conflicts WHERE source_file = ?`, sourceFile)
		return false, derr
	}
	if err != nil {
		return false, err
	}
	existingSrc := existing.String
	if existingSrc == "" {
		existingSrc = model.SourceClaudeCode
	}
	mySrc := source
	if mySrc == "" {
		mySrc = model.SourceClaudeCode
	}
	if existingSrc != mySrc {
		now := time.Now().Unix()
		if _, derr := tx.Exec(`INSERT INTO source_conflicts(source_file, uuid, seen_source, conflicting_source, first_seen_at, last_seen_at)
			VALUES (?,?,?,?,?,?)
			ON CONFLICT(source_file) DO UPDATE SET uuid=excluded.uuid, seen_source=excluded.seen_source,
			conflicting_source=excluded.conflicting_source, last_seen_at=excluded.last_seen_at`,
			sourceFile, uuid, existingSrc, mySrc, now, now); derr != nil {
			return false, derr
		}
		ing.log.Warn("cross-source uuid conflict; file not indexed", "uuid", uuid, "have", existingSrc, "got", mySrc, "file", sourceFile)
		return true, nil
	}
	_, derr := tx.Exec(`DELETE FROM source_conflicts WHERE source_file = ?`, sourceFile)
	return false, derr
}

func (ing *Ingester) commit(kind changeKind, sess model.Session, msgs []model.Message, excludedToolUses []string, fs FileState) (int, error) {
	tx, err := ing.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if ing.preCommitHook != nil {
		ing.preCommitHook()
	}

	// If the file changed since we read it (a concurrent writer committed newer
	// bytes, or a rewrite/truncate happened), committing our stale snapshot
	// could insert phantom rows or revert the DB below disk. Re-stat inside the
	// write lock and abort; the next watcher tick / catch-up re-ingests fresh.
	// A stat error (file removed/replaced) is also "changed" — never commit then.
	fi, statErr := os.Stat(fs.SourceFile)
	if statErr != nil {
		return 0, errStaleSnapshot
	}
	if fi.Size() != fs.LastSize || fi.ModTime().UnixNano() != fs.LastMTime {
		return 0, errStaleSnapshot
	}

	// Cross-source uuid collision, detected inside the write transaction so it is
	// atomic against a racing writer: refuse the file (no session/message rows) and
	// commit only the durable conflict record.
	if conflict, cerr := ing.checkSourceConflict(tx, sess.UUID, sess.Source, fs.SourceFile); cerr != nil {
		return 0, cerr
	} else if conflict {
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, errSourceConflict
	}

	for _, id := range excludedToolUses {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO excluded_tool_uses(tool_use_id) VALUES (?)`, id); err != nil {
			return 0, err
		}
	}

	if kind == changeFull {
		if _, err := tx.Exec(`DELETE FROM tool_calls WHERE message_id IN (SELECT id FROM messages WHERE session_uuid = ?)`, sess.UUID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM tool_targets WHERE session_uuid = ?`, sess.UUID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM messages WHERE session_uuid = ?`, sess.UUID); err != nil {
			return 0, err
		}
	}

	inserted := 0
	for _, m := range msgs {
		res, err := tx.Exec(`INSERT OR IGNORE INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES (?,?,?,?,?,?)`,
			m.SessionUUID, m.Seq, nullZero(m.TS), m.Role, m.Content, m.RawJSON)
		if err != nil {
			return 0, fmt.Errorf("insert message: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		if affected == 0 {
			// Duplicate (session_uuid, seq) already present (concurrent writer or
			// re-ingest). Its tool_calls already exist; do not use the stale
			// LastInsertId.
			continue
		}
		inserted++
		if len(m.ToolCalls) > 0 || len(m.Targets) > 0 {
			id, err := res.LastInsertId()
			if err != nil {
				return 0, err
			}
			for _, tc := range m.ToolCalls {
				if _, err := tx.Exec(`INSERT INTO tool_calls(message_id, tool_name, params_summary) VALUES (?,?,?)`, id, tc.ToolName, tc.ParamsSummary); err != nil {
					return 0, err
				}
			}
			for _, tg := range m.Targets {
				if _, err := tx.Exec(`INSERT INTO tool_targets(message_id, session_uuid, ts, kind, value) VALUES (?,?,?,?,?)`, id, m.SessionUUID, nullZero(m.TS), tg.Kind, tg.Value); err != nil {
					return 0, err
				}
			}
		}
	}

	var totalUserTurns int
	if err := tx.QueryRow(`SELECT count(*) FROM messages WHERE session_uuid = ? AND role = ?`,
		sess.UUID, model.RoleUser).Scan(&totalUserTurns); err != nil {
		return 0, fmt.Errorf("count user turns: %w", err)
	}
	if err := ing.upsertSession(tx, sess, totalUserTurns, kind); err != nil {
		return 0, err
	}

	monotonic := kind != changeFull
	if err := ing.upsertIngestState(tx, fs, monotonic); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// upsertIngestState writes the file watermark into ingest_state. When monotonic
// is true (incremental ingests), the update only applies when the new offset is
// at least as large as the stored one — preventing a stale writer from moving
// the watermark backward.
func (ing *Ingester) upsertIngestState(tx *sql.Tx, fs FileState, monotonic bool) error {
	// unparsed_lines: accumulate across incremental passes (each pass only sees new
	// bytes, so overwriting would lose earlier skips and make doctor go green while
	// content is still missing); on a full reingest, set it to this pass's count.
	unparsedExpr := "excluded.unparsed_lines"
	if monotonic {
		// Accumulate only on a STRICT advance: a re-commit at the same offset (e.g. a
		// concurrent writer landing the same append) must not double-count.
		unparsedExpr = "CASE WHEN excluded.last_byte_offset > ingest_state.last_byte_offset THEN ingest_state.unparsed_lines + excluded.unparsed_lines ELSE ingest_state.unparsed_lines END"
	}
	q := `INSERT INTO ingest_state(source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, head_fingerprint, last_ingested_at, unparsed_lines)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(source_file) DO UPDATE SET last_size=excluded.last_size, last_mtime=excluded.last_mtime,
		last_byte_offset=excluded.last_byte_offset, tail_fingerprint=excluded.tail_fingerprint,
		head_fingerprint=excluded.head_fingerprint, last_ingested_at=excluded.last_ingested_at,
		unparsed_lines=` + unparsedExpr
	if monotonic {
		q += ` WHERE excluded.last_byte_offset >= ingest_state.last_byte_offset`
	}
	_, err := tx.Exec(q, fs.SourceFile, fs.LastSize, fs.LastMTime, fs.LastByteOffset, fs.TailFingerprint, fs.HeadFingerprint, fs.LastIngestedAt, fs.UnparsedLines)
	return err
}

func (ing *Ingester) upsertSession(tx *sql.Tx, s model.Session, userTurns int, kind changeKind) error {
	if kind == changeFull {
		_, err := tx.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title, parent_session, agent_type, source)
			VALUES (?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(uuid) DO UPDATE SET project_path=excluded.project_path, source_file=excluded.source_file,
			started_at=excluded.started_at, ended_at=excluded.ended_at, turn_count=excluded.turn_count, title=excluded.title,
			parent_session=excluded.parent_session, agent_type=excluded.agent_type, source=excluded.source`,
			s.UUID, nullEmpty(s.ProjectPath), s.SourceFile, nullZero(s.StartedAt), nullZero(s.EndedAt), userTurns, nullEmpty(s.Title), nullEmpty(s.ParentSession), nullEmpty(s.AgentType), nullEmpty(s.Source))
		return err
	}
	// Incremental: bump ended_at, set turn_count to the authoritative total, and
	// fill project_path/title/parent_session/agent_type if still missing (subagent
	// metadata can arrive in a later line, e.g. attributionAgent on the assistant turn).
	_, err := tx.Exec(`UPDATE sessions SET
		ended_at = MAX(COALESCE(ended_at,0), ?),
		turn_count = ?,
		project_path = COALESCE(NULLIF(project_path,''), ?),
		title = COALESCE(NULLIF(title,''), ?),
		parent_session = COALESCE(NULLIF(parent_session,''), ?),
		agent_type = COALESCE(NULLIF(agent_type,''), ?),
		source = COALESCE(NULLIF(source,''), ?)
		WHERE uuid = ?`,
		nullZero(s.EndedAt), userTurns, nullEmpty(s.ProjectPath), nullEmpty(s.Title), nullEmpty(s.ParentSession), nullEmpty(s.AgentType), nullEmpty(s.Source), s.UUID)
	if err != nil {
		return err
	}
	// If the session row didn't exist yet (incremental on a brand-new file path
	// whose state was somehow present), ensure it exists.
	_, err = tx.Exec(`INSERT OR IGNORE INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title, parent_session, agent_type, source)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		s.UUID, nullEmpty(s.ProjectPath), s.SourceFile, nullZero(s.StartedAt), nullZero(s.EndedAt), userTurns, nullEmpty(s.Title), nullEmpty(s.ParentSession), nullEmpty(s.AgentType), nullEmpty(s.Source))
	return err
}

// PurgeMissing reconciles the DB against the filesystem and removes rows for source
// files confirmed gone (a not-exist stat result). It is the authoritative deletion path
// (fsnotify Remove/Rename events fire during atomic temp->rename writes, so a raw event
// is not trustworthy). It is partitioned per source root: a root that is missing or
// unreadable is in "preservation mode" — its rows are kept and it never authorizes
// purging another root's rows, so a temporarily-absent root (e.g. Codex not mounted)
// cannot wipe its index. A second guard refuses to purge when the missing set is both a
// large count and most of the candidate corpus (a filesystem problem, not deletions).
func (ing *Ingester) PurgeMissing(ctx context.Context, projectsDir string) error {
	// Determine which source roots are currently available (ReadDir, so an unreadable
	// root counts as unavailable too).
	roots := append([]string{projectsDir}, ing.extraRoots()...)
	var avail []string
	for _, r := range roots {
		if r == "" {
			continue
		}
		if _, err := os.ReadDir(r); err != nil {
			ing.log.Warn("purge: source root unavailable, preserving its rows", "root", r, "err", err)
			continue
		}
		avail = append(avail, r)
	}
	if len(avail) == 0 {
		return nil // no root readable — never read that as "the user deleted everything"
	}

	// A refused (conflicting) file has no ingest_state/sessions row, so it never enters
	// the purge candidate set below; clear its source_conflicts record here if the file
	// is gone (under an available root), so doctor stops reporting a deleted transcript.
	ing.purgeStaleConflicts(avail)

	srcs, err := ing.allSourceFiles()
	if err != nil {
		return err
	}
	// Candidates: tracked files under an available root. Files under a missing root
	// (or attributable to no current root) are preserved.
	var candidates []string
	for _, src := range srcs {
		if pathUnderAny(src, avail) {
			candidates = append(candidates, src)
		}
	}

	var missing []string
	for _, src := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, serr := os.Stat(src); errors.Is(serr, fs.ErrNotExist) {
			missing = append(missing, src)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	// Safety cap: refuse a purge that is BOTH a large absolute count AND most of the
	// candidate corpus (files under available roots) — that pattern is a filesystem
	// problem, not real deletions. A small number of genuine deletions still purges.
	const minPurgeForCap = 10
	if len(missing) > minPurgeForCap && len(missing)*2 > len(candidates) {
		ing.log.Warn("purge refused: most sources missing at once, likely a filesystem problem",
			"missing", len(missing), "candidates", len(candidates))
		return nil
	}

	for _, src := range missing {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ing.purgeSource(src); err != nil {
			ing.log.Warn("purge source failed", "file", src, "err", err)
		}
	}
	return nil
}

// purgeStaleConflicts drops source_conflicts rows whose file (under an available root)
// no longer exists, so doctor doesn't keep reporting a conflict for a deleted
// transcript. A refused file has no ingest_state/sessions row, so it never reaches the
// normal purge path; this is its dedicated cleanup. Files under an unavailable root are
// preserved (the root may just be unmounted).
func (ing *Ingester) purgeStaleConflicts(availRoots []string) {
	rows, err := ing.db.Query(`SELECT source_file FROM source_conflicts`)
	if err != nil {
		ing.log.Warn("read source_conflicts failed", "err", err)
		return
	}
	var stale []string
	for rows.Next() {
		var sf string
		if rows.Scan(&sf) != nil {
			continue
		}
		if !pathUnderAny(sf, availRoots) {
			continue
		}
		if _, serr := os.Stat(sf); errors.Is(serr, fs.ErrNotExist) {
			stale = append(stale, sf)
		}
	}
	rows.Close()
	for _, sf := range stale {
		if _, err := ing.db.Exec(`DELETE FROM source_conflicts WHERE source_file = ?`, sf); err != nil {
			ing.log.Warn("clear stale conflict failed", "file", sf, "err", err)
		}
	}
}

func (ing *Ingester) allSourceFiles() ([]string, error) {
	rows, err := ing.db.Query(`SELECT source_file FROM ingest_state
		UNION SELECT source_file FROM sessions WHERE source_file IS NOT NULL AND source_file <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// purgeSource removes one source's session, messages (FTS via delete triggers), orphaned
// tool_calls, and ingest_state row in a single transaction. It deletes the session/messages
// by the canonical uuid (sessionUUIDFromPath) so they go even if the sessions row is gone
// (ghost state) — BUT only when this src still owns the uuid. If the session row now points
// at a different, re-ingested path (the file was moved/renamed under the same name, e.g. a
// renamed project dir), deleting by uuid would clobber the live data, so only the stale
// ingest_state for src is removed. A final re-stat closes the scan->delete TOCTOU window.
// sessionUUIDForPurge resolves the session uuid for a source path via its owning
// source adapter (Codex's id is not the filename), falling back to the claude-code
// filename convention when no source owns the path.
func (ing *Ingester) sessionUUIDForPurge(src string) string {
	if s := ing.sourceFor(src); s != nil {
		return s.SessionIDFromPath(src)
	}
	return sessionUUIDFromPath(src)
}

func (ing *Ingester) purgeSource(src string) error {
	// Purge only when a fresh stat still confirms the file is gone. Skip on success
	// (reappeared) AND on any non-ErrNotExist error (permission/IO) — those don't prove
	// deletion and must never trigger a purge.
	if _, err := os.Stat(src); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	uuid := ing.sessionUUIDForPurge(src)
	tx, err := ing.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Does this src still own the uuid? Owned when there is no session row (ghost) or its
	// source_file is empty or equals src. If it points elsewhere, the file was moved and
	// re-ingested under that path; don't touch its rows.
	var curSource sql.NullString
	serr := tx.QueryRow(`SELECT source_file FROM sessions WHERE uuid = ?`, uuid).Scan(&curSource)
	if serr != nil && !errors.Is(serr, sql.ErrNoRows) {
		return serr
	}
	owns := errors.Is(serr, sql.ErrNoRows) || !curSource.Valid || curSource.String == "" || curSource.String == src

	if owns {
		if _, err := tx.Exec(`DELETE FROM messages WHERE session_uuid = ?`, uuid); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM tool_calls WHERE message_id NOT IN (SELECT id FROM messages)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM tool_targets WHERE session_uuid = ?`, uuid); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM sessions WHERE uuid = ?`, uuid); err != nil {
			return err
		}
	}
	// Always drop the stale ingest_state row for this exact source path.
	if _, err := tx.Exec(`DELETE FROM ingest_state WHERE source_file = ?`, src); err != nil {
		return err
	}
	return tx.Commit()
}

func (ing *Ingester) loadState(path string) (*FileState, error) {
	var fs FileState
	err := ing.db.QueryRow(`SELECT source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, head_fingerprint, last_ingested_at, unparsed_lines
		FROM ingest_state WHERE source_file = ?`, path).Scan(
		&fs.SourceFile, &fs.LastSize, &fs.LastMTime, &fs.LastByteOffset, &fs.TailFingerprint, &fs.HeadFingerprint, &fs.LastIngestedAt, &fs.UnparsedLines)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &fs, nil
}

func (ing *Ingester) loadExcludedToolUses() ([]string, error) {
	rows, err := ing.db.Query(`SELECT tool_use_id FROM excluded_tool_uses`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (ing *Ingester) maxSeq(sessionUUID string) (int, error) {
	var seq sql.NullInt64
	if err := ing.db.QueryRow(`SELECT MAX(seq) FROM messages WHERE session_uuid = ?`, sessionUUID).Scan(&seq); err != nil {
		return 0, err
	}
	if !seq.Valid {
		return -1, nil // so startSeq becomes 0 after +1
	}
	return int(seq.Int64), nil
}

func nullZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
