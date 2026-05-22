// Package ingest scans Claude Code session files and writes them into the DB.
package ingest

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/model"
)

// preCommitHook, if non-nil, runs inside commit() just after BEGIN and before
// any writes. Tests use it to mutate the on-disk file and exercise the
// re-validation guard. Always nil in production.
var preCommitHook func()

// errStaleSnapshot means the source file changed between read and commit; the
// caller treats it as a no-op and lets a later pass re-ingest the fresh bytes.
var errStaleSnapshot = errors.New("source file changed during ingest")

// Ingester writes parsed sessions into the database.
type Ingester struct {
	db  *db.DB
	log *slog.Logger
}

// New returns an Ingester. If log is nil, logging is discarded.
func New(database *db.DB, log *slog.Logger) *Ingester {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Ingester{db: database, log: log}
}

// Stats summarizes an ingest run.
type Stats struct {
	FilesScanned  int
	FilesIngested int
	FilesSkipped  int
	MessagesAdded int
}

// IngestAll walks projectsDir and ingests every .jsonl file. force re-ingests
// from scratch regardless of stored state.
func (ing *Ingester) IngestAll(projectsDir string, force bool) (Stats, error) {
	files, err := WalkSessionFiles(projectsDir)
	if err != nil {
		return Stats{}, err
	}
	var st Stats
	for _, f := range files {
		st.FilesScanned++
		n, ingested, err := ing.IngestFile(f, force)
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
func (ing *Ingester) IngestFile(path string, force bool) (int, bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false, err
	}
	size, mtime := fi.Size(), fi.ModTime().Unix()

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
		fp, err := fingerprintAt(f, prior.LastByteOffset)
		if err != nil {
			return 0, false, err
		}
		if fp == prior.TailFingerprint {
			startOffset = prior.LastByteOffset
		} else {
			kind = changeFull // bytes before offset changed: rewritten
		}
	}

	buf, err := readFrom(f, startOffset)
	if err != nil {
		return 0, false, err
	}
	completeLen := lastCompleteNewline(buf)
	if completeLen == 0 {
		return 0, false, nil // no complete new line yet
	}
	newOffset := startOffset + int64(completeLen)
	complete := buf[:completeLen]

	startSeq := 0
	if kind == changeIncremental {
		startSeq, err = ing.maxSeq(sessionUUIDFromPath(path))
		if err != nil {
			return 0, false, err
		}
		startSeq++
	}

	parser := NewParser(startSeq)
	if excluded, err := ing.loadExcludedToolUses(); err == nil {
		parser.Seed(excluded)
	}
	msgs, sess := ing.parseBuffer(parser, complete, path)

	newFP, err := fingerprintAt(f, newOffset)
	if err != nil {
		return 0, false, err
	}

	n, err := ing.commit(kind, sess, msgs, parser.ClioToolUseIDs(), FileState{
		SourceFile:      path,
		LastSize:        size,
		LastMTime:       mtime,
		LastByteOffset:  newOffset,
		TailFingerprint: newFP,
		LastIngestedAt:  time.Now().Unix(),
	})
	if err != nil {
		if errors.Is(err, errStaleSnapshot) {
			return 0, false, nil // changed under us; next pass will re-ingest
		}
		return 0, false, err
	}
	return n, true, nil
}

// parseBuffer parses complete lines and accumulates session metadata.
func (ing *Ingester) parseBuffer(p *Parser, complete []byte, path string) ([]model.Message, model.Session) {
	sess := model.Session{UUID: sessionUUIDFromPath(path), SourceFile: path}
	var msgs []model.Message
	for _, line := range splitLines(complete) {
		if len(line) == 0 {
			continue
		}
		lineMsgs, info, err := p.ParseLine(line)
		if err != nil {
			ing.log.Warn("skip malformed line", "file", path, "err", err)
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
		sess.ProjectPath = fallbackProjectPath(parentDirName(path))
	}
	return msgs, sess
}

func (ing *Ingester) commit(kind changeKind, sess model.Session, msgs []model.Message, excludedToolUses []string, fs FileState) (int, error) {
	tx, err := ing.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if preCommitHook != nil {
		preCommitHook()
	}

	// changeFull deletes the session's rows before reinserting. If the file
	// changed since we read it (a concurrent writer committed newer bytes),
	// committing our stale snapshot would revert the DB below disk. Re-stat
	// inside the write lock and abort if it moved.
	if kind == changeFull {
		if fi, statErr := os.Stat(fs.SourceFile); statErr == nil {
			if fi.Size() != fs.LastSize || fi.ModTime().Unix() != fs.LastMTime {
				return 0, errStaleSnapshot
			}
		}
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
		if len(m.ToolCalls) > 0 {
			id, err := res.LastInsertId()
			if err != nil {
				return 0, err
			}
			for _, tc := range m.ToolCalls {
				if _, err := tx.Exec(`INSERT INTO tool_calls(message_id, tool_name, params_summary) VALUES (?,?,?)`, id, tc.ToolName, tc.ParamsSummary); err != nil {
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

	if kind == changeFull {
		if _, err := tx.Exec(`INSERT INTO ingest_state(source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, last_ingested_at)
			VALUES (?,?,?,?,?,?)
			ON CONFLICT(source_file) DO UPDATE SET last_size=excluded.last_size, last_mtime=excluded.last_mtime,
			last_byte_offset=excluded.last_byte_offset, tail_fingerprint=excluded.tail_fingerprint, last_ingested_at=excluded.last_ingested_at`,
			fs.SourceFile, fs.LastSize, fs.LastMTime, fs.LastByteOffset, fs.TailFingerprint, fs.LastIngestedAt); err != nil {
			return 0, err
		}
	} else {
		if _, err := tx.Exec(`INSERT INTO ingest_state(source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, last_ingested_at)
			VALUES (?,?,?,?,?,?)
			ON CONFLICT(source_file) DO UPDATE SET last_size=excluded.last_size, last_mtime=excluded.last_mtime,
			last_byte_offset=excluded.last_byte_offset, tail_fingerprint=excluded.tail_fingerprint, last_ingested_at=excluded.last_ingested_at
			WHERE excluded.last_byte_offset >= ingest_state.last_byte_offset`,
			fs.SourceFile, fs.LastSize, fs.LastMTime, fs.LastByteOffset, fs.TailFingerprint, fs.LastIngestedAt); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

func (ing *Ingester) upsertSession(tx *sql.Tx, s model.Session, userTurns int, kind changeKind) error {
	if kind == changeFull {
		_, err := tx.Exec(`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title, parent_session)
			VALUES (?,?,?,?,?,?,?,?)
			ON CONFLICT(uuid) DO UPDATE SET project_path=excluded.project_path, source_file=excluded.source_file,
			started_at=excluded.started_at, ended_at=excluded.ended_at, turn_count=excluded.turn_count, title=excluded.title`,
			s.UUID, nullEmpty(s.ProjectPath), s.SourceFile, nullZero(s.StartedAt), nullZero(s.EndedAt), userTurns, nullEmpty(s.Title), nullEmpty(s.ParentSession))
		return err
	}
	// Incremental: bump ended_at, set turn_count to the authoritative total,
	// fill project_path/title if missing.
	_, err := tx.Exec(`UPDATE sessions SET
		ended_at = MAX(COALESCE(ended_at,0), ?),
		turn_count = ?,
		project_path = COALESCE(NULLIF(project_path,''), ?),
		title = COALESCE(NULLIF(title,''), ?)
		WHERE uuid = ?`,
		nullZero(s.EndedAt), userTurns, nullEmpty(s.ProjectPath), nullEmpty(s.Title), s.UUID)
	if err != nil {
		return err
	}
	// If the session row didn't exist yet (incremental on a brand-new file path
	// whose state was somehow present), ensure it exists.
	_, err = tx.Exec(`INSERT OR IGNORE INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title)
		VALUES (?,?,?,?,?,?,?)`,
		s.UUID, nullEmpty(s.ProjectPath), s.SourceFile, nullZero(s.StartedAt), nullZero(s.EndedAt), userTurns, nullEmpty(s.Title))
	return err
}

func (ing *Ingester) loadState(path string) (*FileState, error) {
	var fs FileState
	err := ing.db.QueryRow(`SELECT source_file, last_size, last_mtime, last_byte_offset, tail_fingerprint, last_ingested_at
		FROM ingest_state WHERE source_file = ?`, path).Scan(
		&fs.SourceFile, &fs.LastSize, &fs.LastMTime, &fs.LastByteOffset, &fs.TailFingerprint, &fs.LastIngestedAt)
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

func readFrom(f *os.File, offset int64) ([]byte, error) {
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

func splitLines(b []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := range len(b) {
		if b[i] == '\n' {
			lines = append(lines, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		lines = append(lines, b[start:])
	}
	return lines
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
