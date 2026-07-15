// Package eval implements a deterministic, assertion-based retrieval
// regression suite over search.Search and ask.Ask, running under plain
// `go test ./...` (no build tag). It loads a small, hand-written, sanitized
// bilingual fixture corpus into a temp database and asserts per-query binary
// expectations. See
// openspec/changes/2026-07-14-retrieval-eval-and-ask-budget/specs/retrieval-eval/spec.md
// for the full requirements this package implements.
package eval

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/linhn0617/clio/internal/db"
)

//go:embed testdata/corpus/sessions.jsonl testdata/search_queries.json testdata/ask_queries.json
var testdataFS embed.FS

// corpusMessage is one message in a fixture session. OffsetDays is a fixed
// relative age (in days, from corpus-load time) rather than an absolute
// timestamp, so recencyBonus-driven ordering (internal/search/rank.go) is
// stable across runs regardless of wall-clock time.
type corpusMessage struct {
	Seq        int    `json:"seq"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	OffsetDays int    `json:"offset_days"`
}

// corpusSession is one fixture session: metadata plus its messages, as one
// line of testdata/corpus/sessions.jsonl.
type corpusSession struct {
	UUID     string          `json:"session"`
	Project  string          `json:"project"`
	Title    string          `json:"title"`
	Source   string          `json:"source"` // "" -> NULL, matching db.SourceFilter's claude-code default
	Messages []corpusMessage `json:"messages"`
}

// loadCorpus reads the embedded JSONL fixture corpus and inserts it into
// database using the same db.Open + INSERT pattern internal/ask's and
// internal/search's own tests use for their temp DBs (see ask_test.go's
// addSession/addMsg, search_test.go's addSession/addMsg). Each message's
// ts is set to now - offset_days*86400.
func loadCorpus(database *db.DB) error {
	raw, err := testdataFS.ReadFile("testdata/corpus/sessions.jsonl")
	if err != nil {
		return fmt.Errorf("read corpus fixture: %w", err)
	}

	now := time.Now()
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var s corpusSession
		if err := json.Unmarshal(line, &s); err != nil {
			return fmt.Errorf("corpus line %d: %w", lineNo, err)
		}
		if err := insertCorpusSession(database, s, now); err != nil {
			return fmt.Errorf("corpus line %d (session %s): %w", lineNo, s.UUID, err)
		}
	}
	return scanner.Err()
}

func insertCorpusSession(database *db.DB, s corpusSession, now time.Time) error {
	var sourceArg any
	if s.Source != "" {
		sourceArg = s.Source
	} // else leave nil -> NULL, which db.SourceFilter treats as claude-code

	nowUnix := now.Unix()
	if _, err := database.Exec(
		`INSERT INTO sessions(uuid, project_path, source_file, started_at, ended_at, turn_count, title, source) VALUES (?,?,?,?,?,?,?,?)`,
		s.UUID, s.Project, s.UUID+".jsonl", nowUnix, nowUnix, len(s.Messages), s.Title, sourceArg,
	); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	for _, m := range s.Messages {
		ts := now.Add(-time.Duration(m.OffsetDays) * 24 * time.Hour).Unix()
		if _, err := database.Exec(
			`INSERT INTO messages(session_uuid, seq, ts, role, content, raw_json) VALUES (?,?,?,?,?,?)`,
			s.UUID, m.Seq, ts, m.Role, m.Content, "{}",
		); err != nil {
			return fmt.Errorf("insert message session=%s seq=%d: %w", s.UUID, m.Seq, err)
		}
	}
	return nil
}

// expectItem is one binary expectation within a query case: an item the
// suite requires to appear within TopK. Seq is used by Search expectations
// (session, seq) and HitSeq by Ask expectations (session, optionally the seq
// that must be marked IsHit within that session's group).
type expectItem struct {
	Session string `json:"session"`
	Seq     int    `json:"seq"`
	HitSeq  *int   `json:"hit_seq"`
	TopK    int    `json:"top_k"`
}

// queryCase is one query and its binary expectations, per the schema in
// design.md's "Expectation schema" section.
type queryCase struct {
	ID     string       `json:"id"`
	Query  string       `json:"query"`
	Lang   string       `json:"lang"`
	Expect []expectItem `json:"expect"`
}

type queryFile struct {
	Queries []queryCase `json:"queries"`
}

func loadQueryFile(name string) (queryFile, error) {
	raw, err := testdataFS.ReadFile("testdata/" + name)
	if err != nil {
		return queryFile{}, fmt.Errorf("read %s: %w", name, err)
	}
	var qf queryFile
	if err := json.Unmarshal(raw, &qf); err != nil {
		return queryFile{}, fmt.Errorf("parse %s: %w", name, err)
	}
	return qf, nil
}
