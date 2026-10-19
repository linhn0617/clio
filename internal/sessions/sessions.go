// Package sessions provides read queries over sessions and messages, shared by
// the CLI (list/show) and the MCP server (list_sessions/read_session).
package sessions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/termsafe"
	"github.com/linhn0617/clio/internal/timeutil"
)

// Session is a row of the sessions table.
type Session struct {
	UUID          string
	ProjectPath   string
	Title         string
	StartedAt     int64
	EndedAt       int64
	TurnCount     int
	ParentSession string // the spawning session's uuid, for a subagent transcript
	AgentType     string // subagent type (e.g. general-purpose); empty for a normal session
	SubagentCount int    // number of subagent children (top-level rows only)
	Source        string // originating tool: "claude-code" | "codex"
}

// Message is a row of the messages table.
type Message struct {
	Seq     int
	TS      int64
	Role    string
	Content string
	RawJSON string
}

// ListFilter narrows ListSessions.
type ListFilter struct {
	Since int64
	// ProjectPrefix keeps sessions whose project is this path or lies beneath it
	// (a "/" boundary: /x/foo does not match /x/foo-bar).
	ProjectPrefix string
	MinTurns      int
	Limit         int
	Touched       string // only sessions whose tool calls touched a file under this path prefix
	Tool          string // only sessions that used this tool (exact name)
	Ran           string // only sessions that ran a command containing this substring
	TargetKind    string // with TargetValue: only sessions with a tool_targets row of this exact kind
	TargetValue   string // with TargetKind: only sessions with a tool_targets row of this exact value
	// IncludeSubagents lists subagent child sessions alongside top-level ones; by
	// default only top-level sessions are returned (children are nested under their
	// parent). ParentSession instead lists exactly one parent's children.
	IncludeSubagents bool
	ParentSession    string
	// Source restricts the listing to one originating tool: "" / "claude-code"
	// (default) lists Claude Code only, "codex" only Codex, "all" every source.
	Source string
	// ExcludeSession drops this session and its subagent children: the recall
	// hook fires for the session being opened, which must not recall itself.
	ExcludeSession string
}

// ListSessions returns sessions matching filter, most recent first.
func ListSessions(ctx context.Context, database *db.DB, f ListFilter) ([]Session, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	filterSQL, filterArgs := listFilters(f)
	q := `SELECT uuid, COALESCE(project_path,''), COALESCE(title,''), COALESCE(started_at,0), COALESCE(ended_at,0), turn_count,
		COALESCE(parent_session,''), COALESCE(agent_type,''), COALESCE(source,'claude-code'),
		(SELECT COUNT(*) FROM sessions sub WHERE sub.parent_session = sessions.uuid)
		FROM sessions WHERE 1=1` + filterSQL
	args := append([]any{}, filterArgs...)

	// Subagent nesting: by default list only top-level sessions. A subagent child is
	// hidden when its parent is indexed and passes the same filters, wherever that
	// parent would rank: scoping the check to the current page made a child resurface
	// whenever its parent merely fell below LIMIT. A child whose parent is filtered
	// out (e.g. older than --since) or not indexed is promoted, not lost.
	// ParentSession instead lists exactly one parent's children.
	if f.ParentSession != "" {
		q += " AND parent_session = ?"
		args = append(args, f.ParentSession)
	} else if !f.IncludeSubagents {
		q += " AND (parent_session IS NULL OR parent_session = '' OR parent_session NOT IN (SELECT uuid FROM sessions WHERE 1=1" + filterSQL + "))"
		args = append(args, filterArgs...)
	}
	// uuid is a deterministic tiebreaker so ties at the LIMIT boundary resolve
	// identically across calls.
	q += " ORDER BY ended_at DESC, uuid DESC LIMIT ?"
	args = append(args, f.Limit)

	rows, err := database.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.UUID, &s.ProjectPath, &s.Title, &s.StartedAt, &s.EndedAt, &s.TurnCount,
			&s.ParentSession, &s.AgentType, &s.Source, &s.SubagentCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// listFilters builds the shared WHERE predicates (everything except the subagent
// nesting clause and LIMIT), so the same filters scope both the listing and the
// "is this child's parent indexed and visible under these filters?" subquery
// used for nesting.
func listFilters(f ListFilter) (string, []any) {
	q := ""
	var args []any
	if f.Since > 0 {
		q += " AND ended_at >= ?"
		args = append(args, f.Since)
	}
	if f.ProjectPrefix != "" {
		clause, pargs := pathUnderClause("project_path", f.ProjectPrefix)
		q += clause
		args = append(args, pargs...)
	}
	if f.ExcludeSession != "" {
		q += ` AND uuid <> ? AND COALESCE(parent_session,'') <> ?`
		args = append(args, f.ExcludeSession, f.ExcludeSession)
	}
	if f.MinTurns > 0 {
		q += " AND turn_count >= ?"
		args = append(args, f.MinTurns)
	}
	if f.Touched != "" {
		q += ` AND uuid IN (SELECT session_uuid FROM tool_targets WHERE kind='file' AND value LIKE ? ESCAPE '\')`
		args = append(args, db.EscapeLike(f.Touched)+"%")
	}
	if f.Tool != "" {
		q += ` AND uuid IN (SELECT session_uuid FROM tool_targets WHERE kind='tool' AND value = ?)`
		args = append(args, f.Tool)
	}
	if f.Ran != "" {
		q += ` AND uuid IN (SELECT session_uuid FROM tool_targets WHERE kind='command' AND value LIKE ? ESCAPE '\')`
		args = append(args, "%"+db.EscapeLike(f.Ran)+"%")
	}
	if f.TargetKind != "" && f.TargetValue != "" {
		q += ` AND uuid IN (SELECT session_uuid FROM tool_targets WHERE kind = ? AND value = ?)`
		args = append(args, f.TargetKind, f.TargetValue)
	}
	if clause, cargs := db.SourceFilter("source", f.Source); clause != "" {
		q += clause
		args = append(args, cargs...)
	}
	return q, args
}

// pathUnderClause matches col equal to path or anything beneath it. A bare
// LIKE 'path%' also caught siblings sharing the prefix (/x/foo-bar for /x/foo).
// A trailing "/" is ignored so "/x/foo/" and "/x/foo" agree. It compares with
// substr rather than LIKE: LIKE folds ASCII case, so on a case-sensitive
// filesystem /x/foo would also match /x/FOO/sub. substr and length both count
// characters, so a non-ASCII prefix lines up.
func pathUnderClause(col, path string) (string, []any) {
	p := strings.TrimRight(path, "/")
	if p == "" {
		return ` AND substr(` + col + `, 1, 1) = '/'`, nil // the filesystem root: every absolute path
	}
	return ` AND (` + col + ` = ? OR substr(` + col + `, 1, length(?)) = ?)`, []any{p, p + "/", p + "/"}
}

// ErrNotFound / ErrAmbiguous / ErrEmptyID report prefix resolution failures.
var (
	ErrNotFound  = errors.New("no session matches that id")
	ErrAmbiguous = errors.New("ambiguous session id prefix")
	ErrEmptyID   = errors.New("session id is empty")
)

const sessionCols = `uuid, COALESCE(project_path,''), COALESCE(title,''), COALESCE(started_at,0), COALESCE(ended_at,0), turn_count, COALESCE(parent_session,''), COALESCE(agent_type,''), COALESCE(source,'claude-code')`

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var s Session
	err := sc.Scan(&s.UUID, &s.ProjectPath, &s.Title, &s.StartedAt, &s.EndedAt, &s.TurnCount, &s.ParentSession, &s.AgentType, &s.Source)
	return s, err
}

// GetSession returns the session with exactly this uuid, in any source, or
// ErrNotFound. Unlike ResolvePrefix it never treats the id as a prefix.
func GetSession(ctx context.Context, q Querier, uuid string) (Session, error) {
	s, err := scanSession(q.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE uuid = ?`, uuid))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return s, err
}

// ResolvePrefix resolves a full uuid or unambiguous prefix to a Session. An exact
// uuid match is honored regardless of source (an exact id is explicit intent); a
// prefix match is restricted to the requested source ("" / "claude-code" default),
// so a short prefix never reaches across tools unless asked. An ambiguous prefix
// returns an *AmbiguousError listing the candidates. All reads share one
// snapshot, so a concurrent ingest cannot make the listing disagree with the
// ambiguity it reports.
func ResolvePrefix(ctx context.Context, database *db.DB, prefix, source string) (Session, error) {
	// An empty or blank prefix would match every session; it is a missing
	// argument, not a request to list the newest ones.
	if strings.TrimSpace(prefix) == "" {
		return Session{}, ErrEmptyID
	}
	tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback() // read-only: nothing to commit
	// Exact match wins regardless of how many prefixes also match, and regardless of source.
	s, err := GetSession(ctx, tx, prefix)
	switch {
	case err == nil:
		return s, nil
	case !errors.Is(err, ErrNotFound):
		return Session{}, err
	}
	// No exact match: resolve by unique prefix (escaped), scoped to the requested
	// source. Two rows are enough to detect ambiguity, so the common unique case
	// stops early; only an ambiguous prefix pays for the ordered candidate query.
	srcClause, srcArgs := db.SourceFilter("source", source)
	like := db.EscapeLike(prefix) + "%"
	matches, err := querySessions(ctx, tx, `SELECT `+sessionCols+` FROM sessions WHERE uuid LIKE ? ESCAPE '\'`+srcClause+` LIMIT 2`,
		append([]any{like}, srcArgs...)...)
	if err != nil {
		return Session{}, err
	}
	switch len(matches) {
	case 0:
		return Session{}, ErrNotFound
	case 1:
		return matches[0], nil
	}
	return Session{}, ambiguous(ctx, tx, prefix, like, srcClause, srcArgs)
}

func querySessions(ctx context.Context, q Querier, query string, args ...any) ([]Session, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		m, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ambiguous builds the *AmbiguousError for a prefix known to match several
// sessions: top-level sessions before subagents, each group newest first.
func ambiguous(ctx context.Context, q Querier, prefix, like, srcClause string, srcArgs []any) error {
	args := append(append([]any{like}, srcArgs...), maxAmbiguousCandidates+1)
	matches, err := querySessions(ctx, q, `SELECT `+sessionCols+` FROM sessions WHERE uuid LIKE ? ESCAPE '\'`+srcClause+
		` ORDER BY COALESCE(parent_session,'') <> '', COALESCE(started_at,0) DESC, uuid LIMIT ?`, args...)
	if err != nil {
		return err
	}
	amb := &AmbiguousError{Prefix: prefix}
	if len(matches) > maxAmbiguousCandidates {
		matches, amb.More = matches[:maxAmbiguousCandidates], true
	}
	// Every session a shortened id could reach starts with prefix, so one read of
	// the prefix's matches (all sources: an exact id wins across sources) is
	// enough to pick each id.
	rows, err := q.QueryContext(ctx, `SELECT uuid, CASE WHEN 1=1`+srcClause+` THEN 1 ELSE 0 END FROM sessions WHERE uuid LIKE ? ESCAPE '\'`,
		append(append([]any{}, srcArgs...), like)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var inScope []string
	exact := map[string]bool{}
	bytewise := true // byte comparison agrees with SQLite LIKE for these ids
	for rows.Next() {
		var u string
		var scoped bool
		if err := rows.Scan(&u, &scoped); err != nil {
			return err
		}
		exact[u] = true
		if scoped {
			inScope = append(inScope, u)
			bytewise = bytewise && likeSafe(u)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range matches {
		id := m.UUID // the whole id always resolves: an exact match wins
		if bytewise {
			id = resolvableID(m.UUID, inScope, exact)
		}
		amb.Candidates = append(amb.Candidates, Candidate{Session: m, ID: id})
	}
	return amb
}

// resolvableID is the shortest prefix of uuid (at least 8 characters, or the
// whole id) that ResolvePrefix resolves back to this session: no other in-scope
// session starts with it (LIKE folds ASCII case) and no session in any source
// has it as its exact id. inScope holds every in-scope match of the typed
// prefix (at least two), so the shared prefix already covers the typed one.
// Callers use it only when every in-scope id is likeSafe, where LIKE matching
// equals ASCII-folded byte-prefix matching.
func resolvableID(uuid string, inScope []string, exact map[string]bool) string {
	low := asciiLower(uuid)
	longest := 0 // longest common prefix, in bytes, with another in-scope session
	for _, u := range inScope {
		if u == uuid {
			continue
		}
		lu := asciiLower(u)
		n := 0
		for n < len(low) && n < len(lu) && low[n] == lu[n] {
			n++
		}
		longest = max(longest, n)
	}
	chars := 0
	for i := 0; i < len(uuid); {
		_, size := utf8.DecodeRuneInString(uuid[i:])
		i += size
		chars++
		if chars >= 8 && i > longest && i < len(uuid) && !exact[uuid[:i]] {
			return uuid[:i]
		}
	}
	return uuid
}

// likeSafe reports whether SQLite's LIKE sees id as the same characters Go's
// byte comparison does: valid UTF-8 with no U+FFFD. SQLite decodes invalid
// sequences differently (several byte patterns read as U+FFFD, a lead byte
// absorbs following bytes), so a shortened id computed bytewise could fail to
// resolve; such prefixes are listed with whole ids instead.
func likeSafe(id string) bool {
	return utf8.ValidString(id) && !strings.ContainsRune(id, utf8.RuneError)
}

// asciiLower folds ASCII letters only, matching SQLite's default LIKE.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// maxAmbiguousCandidates caps how many sessions an ambiguous prefix lists.
const maxAmbiguousCandidates = 10

// Candidate is one session an ambiguous prefix matched, with ID, a prefix of
// its uuid that resolves to it alone.
type Candidate struct {
	Session
	ID string
}

// AmbiguousError is returned for a prefix that matches several sessions. It
// matches ErrAmbiguous under errors.Is. Callers render it with their own time and
// title formatting via Render; Error uses RFC 3339 times.
type AmbiguousError struct {
	Prefix     string
	Candidates []Candidate
	More       bool // more sessions match than Candidates holds
}

func (e *AmbiguousError) Is(target error) bool { return target == ErrAmbiguous }

func (e *AmbiguousError) Error() string {
	return e.Render(timeutil.FormatRFC3339, func(t string) string { return termsafe.OneLine(t, 60) })
}

// Render lists the candidates, one per line, with the caller's time and title
// formatting. Ids are padded to one width; an id that is not plain printable
// text is shown quoted with escapes, so it stays visible and distinct.
func (e *AmbiguousError) Render(formatTime func(int64) string, formatTitle func(string) string) string {
	ids := make([]string, len(e.Candidates))
	times := make([]string, len(e.Candidates))
	idWidth, timeWidth := 0, 0
	for i, c := range e.Candidates {
		ids[i] = displayID(c.ID)
		times[i] = formatTime(c.StartedAt)
		idWidth = max(idWidth, utf8.RuneCountInString(ids[i]))
		timeWidth = max(timeWidth, utf8.RuneCountInString(times[i]))
	}
	pad := func(s string, w int) string { return s + strings.Repeat(" ", w-utf8.RuneCountInString(s)) }
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s matches several sessions; use a longer id:", ErrAmbiguous, strconv.Quote(e.Prefix))
	for i, c := range e.Candidates {
		tag := ""
		if c.ParentSession != "" {
			tag = "[subagent] "
		}
		fmt.Fprintf(&b, "\n  %s  %s  %s%s", pad(ids[i], idWidth), pad(times[i], timeWidth), tag, formatTitle(c.Title))
	}
	if e.More {
		fmt.Fprintf(&b, "\n  (more than %d match)", maxAmbiguousCandidates)
	}
	return b.String()
}

// displayID returns id unchanged when every rune is printable and not a space
// (zero-width and bidi format characters are not), otherwise its quoted
// Go-escaped form.
func displayID(id string) string {
	if utf8.ValidString(id) && termsafe.Strip(id) == id {
		plain := true
		for _, r := range id {
			if r == ' ' || !unicode.IsPrint(r) {
				plain = false
				break
			}
		}
		if plain {
			return id
		}
	}
	return strconv.Quote(id)
}

// rawColumn selects raw_json only when the caller needs it; otherwise an empty
// string in its place, so high-frequency readers (e.g. the TUI preview) don't
// pull the largest column off disk for content they never render.
func rawColumn(includeRaw bool) string {
	if includeRaw {
		return "raw_json"
	}
	return "''"
}

// Querier is the read surface the message helpers below need: a *db.DB, or a
// *sql.Tx when several reads must see one snapshot.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// GetMessages returns a session's messages ordered by seq, paginated.
// When includeToolOutput is false, tool_use/tool_result/thinking are omitted.
// When includeRaw is false, raw_json is not read (Message.RawJSON stays empty).
// Returns the page and whether more rows exist past offset+limit.
func GetMessages(ctx context.Context, database Querier, sessionUUID string, offset, limit int, includeToolOutput, includeRaw bool) ([]Message, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT seq, COALESCE(ts,0), role, content, ` + rawColumn(includeRaw) + ` FROM messages WHERE session_uuid = ?`
	if !includeToolOutput {
		q += " AND role IN ('user','assistant')"
	}
	q += " ORDER BY seq LIMIT ? OFFSET ?"
	rows, err := database.QueryContext(ctx, q, sessionUUID, limit+1, offset) // +1 to detect more
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Seq, &m.TS, &m.Role, &m.Content, &m.RawJSON); err != nil {
			return nil, false, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// CountMessages counts a session's messages under the same role filter as
// GetMessages. With beforeSeq non-nil only messages with seq < *beforeSeq are
// counted, which is the zero-based position GetMessages' offset would use to
// start at that seq.
func CountMessages(ctx context.Context, database Querier, sessionUUID string, includeToolOutput bool, beforeSeq *int) (int, error) {
	q := `SELECT COUNT(*) FROM messages WHERE session_uuid = ?`
	args := []any{sessionUUID}
	if !includeToolOutput {
		q += " AND role IN ('user','assistant')"
	}
	if beforeSeq != nil {
		q += " AND seq < ?"
		args = append(args, *beforeSeq)
	}
	var n int
	err := database.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// MaxSeq returns the highest message seq of a session over all roles, or -1
// when it has no messages.
func MaxSeq(ctx context.Context, database Querier, sessionUUID string) (int, error) {
	var n int
	err := database.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), -1) FROM messages WHERE session_uuid = ?`, sessionUUID).Scan(&n)
	return n, err
}

// MessageRole returns the role of a session's message at seq, or "" when no
// message has that seq.
func MessageRole(ctx context.Context, database Querier, sessionUUID string, seq int) (string, error) {
	var role string
	err := database.QueryRowContext(ctx, `SELECT role FROM messages WHERE session_uuid = ? AND seq = ?`, sessionUUID, seq).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return role, err
}

// GetWindow returns a conversational window around a retrieval hit: up to `before`
// dialogue messages preceding the hit at hitSeq, the hit itself, and up to `after`
// following ones, ordered by seq. The window is taken in user/assistant turn space
// (when includeToolOutput is false), so tool_use/tool_result events between a
// question and its answer neither consume the window nor appear in it.
func GetWindow(ctx context.Context, database *db.DB, sessionUUID string, hitSeq, before, after int, includeToolOutput, includeRaw bool) ([]Message, error) {
	if before < 0 {
		before = 0
	}
	if after < 0 {
		after = 0
	}
	roleClause := ""
	if !includeToolOutput {
		roleClause = " AND role IN ('user','assistant')"
	}
	cols := `seq, COALESCE(ts,0), role, content, ` + rawColumn(includeRaw)

	// The hit and preceding turns, newest-first, reversed to ascending.
	pre, err := windowRows(ctx, database,
		`SELECT `+cols+` FROM messages WHERE session_uuid = ?`+roleClause+` AND seq <= ? ORDER BY seq DESC LIMIT ?`,
		sessionUUID, hitSeq, before+1)
	if err != nil {
		return nil, err
	}
	slices.Reverse(pre)

	// Following turns, ascending.
	post, err := windowRows(ctx, database,
		`SELECT `+cols+` FROM messages WHERE session_uuid = ?`+roleClause+` AND seq > ? ORDER BY seq ASC LIMIT ?`,
		sessionUUID, hitSeq, after)
	if err != nil {
		return nil, err
	}
	return append(pre, post...), nil
}

func windowRows(ctx context.Context, database *db.DB, query string, args ...any) ([]Message, error) {
	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Seq, &m.TS, &m.Role, &m.Content, &m.RawJSON); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ActivityCount is one grouped activity value with its occurrence count.
type ActivityCount struct {
	Value string
	Count int
}

// ActivityByKind returns the most frequent activity values of a kind
// (file|command|tool|pattern|url), optionally bounded by time and project (the
// project path itself or anything beneath it).
func ActivityByKind(ctx context.Context, database *db.DB, kind string, since int64, projectPrefix, source string, limit int) ([]ActivityCount, error) {
	return activityByKind(ctx, database, activityQuery{kind: kind, since: since, project: projectPrefix, source: source, limit: limit})
}

// activityQuery carries ActivityByKind's filters plus the recall-only ones.
type activityQuery struct {
	kind           string
	since          int64
	project        string
	source         string
	excludeSession string // drop this session and its subagents
	valueUnder     string // keep only values that are this path or beneath it
	limit          int
}

func activityByKind(ctx context.Context, database *db.DB, a activityQuery) ([]ActivityCount, error) {
	limit := a.limit
	if limit <= 0 {
		limit = 50
	}
	srcClause, srcArgs := db.SourceFilter("s.source", a.source)
	q := `SELECT tt.value, COUNT(*) FROM tool_targets tt`
	if a.project != "" || srcClause != "" || a.excludeSession != "" {
		q += ` JOIN sessions s ON s.uuid = tt.session_uuid`
	}
	q += ` WHERE tt.kind = ?`
	args := []any{a.kind}
	if a.project != "" {
		clause, pargs := pathUnderClause("s.project_path", a.project)
		q += clause
		args = append(args, pargs...)
	}
	if a.excludeSession != "" {
		q += ` AND s.uuid <> ? AND COALESCE(s.parent_session,'') <> ?`
		args = append(args, a.excludeSession, a.excludeSession)
	}
	if a.valueUnder != "" {
		// Filtered in SQL so LIMIT counts only the kept values.
		clause, vargs := pathUnderClause("tt.value", a.valueUnder)
		q += clause
		args = append(args, vargs...)
	}
	if a.since > 0 {
		q += ` AND tt.ts >= ?`
		args = append(args, a.since)
	}
	if srcClause != "" {
		q += srcClause
		args = append(args, srcArgs...)
	}
	q += ` GROUP BY tt.value ORDER BY COUNT(*) DESC, tt.value LIMIT ?`
	args = append(args, limit)

	rows, err := database.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivityCount
	for rows.Next() {
		var a ActivityCount
		if err := rows.Scan(&a.Value, &a.Count); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// TailExcerpt is the last assistant text message of a session: where Claude's
// own closing words say the session left off.
type TailExcerpt struct {
	SessionUUID string
	TS          int64
	Text        string
}

// Recall bundles a project's recent sessions, recently touched files, and
// recently run commands — the material for clio's session-start recall digest.
// LastAssistant is the newest listed session's closing assistant text, nil
// when that session has none.
type Recall struct {
	LastAssistant *TailExcerpt
	Sessions      []Session
	Files         []ActivityCount
	Commands      []ActivityCount
}

// GetRecall gathers a project's recent sessions plus its most-touched files and
// most-run commands since the given time. excludeSession ("" = none) drops the
// session being opened, and its subagents, from every section; touched files
// are limited to paths under the project.
func GetRecall(ctx context.Context, database *db.DB, projectPrefix, excludeSession string, since int64, limitSessions, limitActivity int) (Recall, error) {
	sess, err := ListSessions(ctx, database, ListFilter{ProjectPrefix: projectPrefix, Since: since, Limit: limitSessions, ExcludeSession: excludeSession})
	if err != nil {
		return Recall{}, err
	}
	// Recall is Claude-Code-only by policy: pass the default ("") source.
	// Files outside the project (scratchpads, other repos a session peeked at)
	// say nothing about where this project left off.
	files, err := activityByKind(ctx, database, activityQuery{kind: "file", since: since, project: projectPrefix,
		excludeSession: excludeSession, valueUnder: projectPrefix, limit: limitActivity})
	if err != nil {
		return Recall{}, err
	}
	commands, err := activityByKind(ctx, database, activityQuery{kind: "command", since: since, project: projectPrefix,
		excludeSession: excludeSession, limit: limitActivity})
	if err != nil {
		return Recall{}, err
	}
	r := Recall{Sessions: sess, Files: files, Commands: commands}
	// The excerpt follows the digest's own first row so the two never disagree;
	// only when the since window is empty does it fall back to the project's
	// newest session regardless of `since` — after a long break is exactly when
	// "where did we leave off" matters most. The fallback keeps the digest's page
	// size so both paths read the same number of rows.
	newest := sess
	if len(newest) == 0 {
		newest, err = ListSessions(ctx, database, ListFilter{ProjectPrefix: projectPrefix, Limit: limitSessions, ExcludeSession: excludeSession})
		if err != nil {
			return Recall{}, err
		}
	}
	if len(newest) > 0 {
		// role='assistant' rows carry text only; tool_use/thinking are separate
		// roles. Blank rows cannot come from clio's own parsers (they drop empty
		// content) but are skipped anyway so a stray one never hides real text.
		var e TailExcerpt
		err := database.QueryRowContext(ctx,
			`SELECT session_uuid, COALESCE(ts,0), content FROM messages WHERE session_uuid = ? AND role = 'assistant' AND TRIM(content) <> '' ORDER BY seq DESC LIMIT 1`,
			newest[0].UUID).Scan(&e.SessionUUID, &e.TS, &e.Text)
		switch {
		case err == nil:
			if e.TS == 0 {
				e.TS = newest[0].EndedAt // undated message row: fall back to the session's end
			}
			r.LastAssistant = &e
		case err != sql.ErrNoRows:
			return Recall{}, err
		}
	}
	return r, nil
}

// Bucket is one row of an activity summary.
type Bucket struct {
	Key      string
	Sessions int
	Messages int
}

// ActivitySummary aggregates activity since a time, grouped by "day" or "project",
// optionally bounded to sessions whose project_path is projectPrefix or lies
// beneath it (matching ActivityByKind's project-filter semantics).
func ActivitySummary(ctx context.Context, database *db.DB, since int64, groupBy, projectPrefix, source string) ([]Bucket, error) {
	var keyExpr string
	switch groupBy {
	case "project":
		keyExpr = "COALESCE(s.project_path,'(unknown)')"
	case "day", "":
		keyExpr = "date(s.ended_at,'unixepoch','localtime')"
	default:
		return nil, fmt.Errorf("invalid group_by %q", groupBy)
	}
	// COALESCE to the parent's uuid only when that parent actually exists (p.uuid),
	// so a parent + its subagents count once, while orphan subagents of an absent
	// parent each count on their own.
	srcClause, srcArgs := db.SourceFilter("s.source", source)
	q := `SELECT ` + keyExpr + ` AS k, COUNT(DISTINCT COALESCE(p.uuid, s.uuid)), COUNT(m.id)
		FROM sessions s
		LEFT JOIN messages m ON m.session_uuid = s.uuid
		LEFT JOIN sessions p ON p.uuid = s.parent_session
		WHERE s.ended_at >= ?`
	args := []any{since}
	if projectPrefix != "" {
		clause, pargs := pathUnderClause("s.project_path", projectPrefix)
		q += clause
		args = append(args, pargs...)
	}
	q += srcClause + `
		GROUP BY k ORDER BY k DESC`
	args = append(args, srcArgs...)
	rows, err := database.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.Key, &b.Sessions, &b.Messages); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
