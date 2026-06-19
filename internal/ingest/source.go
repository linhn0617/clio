package ingest

import (
	"os"
	"strings"

	"github.com/linhn0617/clio/internal/config"
	"github.com/linhn0617/clio/internal/model"
)

// Source is a pluggable ingestion adapter for one agent tool's transcripts. It owns
// file ownership, the canonical session id, the extra roots to walk, and the
// whole-file parse + session-metadata aggregation. The incremental/commit/FTS/redact
// machinery is shared and format-agnostic. Exactly one source owns any discovered
// file; the claude-code source is the fallback, consulted last.
type Source interface {
	Name() string
	Owns(path string) bool
	SessionIDFromPath(path string) string
	// Roots returns extra directories this source contributes to a full scan, beyond
	// the Claude Code projects dir passed to IngestAll. The claude-code source returns
	// none (it is walked via that projects dir).
	Roots() ([]string, error)
	// ParseFile parses one source file region into a session and its messages. It owns
	// the per-line parse and metadata aggregation for its format.
	ParseFile(ing *Ingester, f *os.File, startOffset int64, startSeq int, path string) (parseResult, error)
}

// parseResult is one source file parsed into a session and its messages.
type parseResult struct {
	Session  model.Session
	Messages []model.Message
	ClioIDs  []string // clio MCP tool_use ids to exclude from the index (claude-code only)
	Consumed int64
	Unparsed int64
}

// claudeCodeSource ingests Claude Code transcripts (~/.claude/projects/**/*.jsonl,
// including subagent transcripts). It is the default/fallback source: its parse logic
// is the existing streamParse, unchanged.
type claudeCodeSource struct{}

func (claudeCodeSource) Name() string { return model.SourceClaudeCode }

// Owns matches any .jsonl path; the claude-code source is the fallback, so more
// specific sources (e.g. codex) are consulted first by sourceFor.
func (claudeCodeSource) Owns(path string) bool { return strings.HasSuffix(path, ".jsonl") }

func (claudeCodeSource) SessionIDFromPath(path string) string { return sessionUUIDFromPath(path) }

func (claudeCodeSource) Roots() ([]string, error) { return nil, nil }

func (claudeCodeSource) ParseFile(ing *Ingester, f *os.File, startOffset int64, startSeq int, path string) (parseResult, error) {
	parser := NewParser(startSeq)
	if excluded, err := ing.loadExcludedToolUses(); err != nil {
		ing.log.Warn("load excluded tool uses failed", "err", err)
	} else {
		parser.Seed(excluded)
	}
	msgs, sess, consumed, unparsed, err := ing.streamParse(f, startOffset, parser, path)
	if err != nil {
		return parseResult{}, err
	}
	return parseResult{Session: sess, Messages: msgs, ClioIDs: parser.ClioToolUseIDs(), Consumed: consumed, Unparsed: unparsed}, nil
}

// pathUnderAny reports whether path lies within any of roots.
func pathUnderAny(path string, roots []string) bool {
	for _, r := range roots {
		if pathUnder(path, r) {
			return true
		}
	}
	return false
}

// sourceFor returns the registered source that owns path (more specific sources first;
// the claude-code fallback owns any remaining .jsonl), or nil when none does.
func (ing *Ingester) sourceFor(path string) Source {
	for _, s := range ing.sources {
		if s.Owns(path) {
			return s
		}
	}
	return nil
}

// extraRoots is the union of registered sources' extra roots, for IngestAll and
// PurgeMissing to span every source. A source whose root is unavailable is skipped
// (logged), never treated as an error.
func (ing *Ingester) extraRoots() []string {
	var out []string
	for _, s := range ing.sources {
		roots, err := s.Roots()
		if err != nil {
			ing.log.Warn("source roots unavailable", "source", s.Name(), "err", err)
			continue
		}
		out = append(out, roots...)
	}
	return out
}

// AddCodexSource registers the Codex CLI source rooted at ~/.codex/sessions, but only
// when that directory exists. Codex not being installed is not an error: the source is
// simply not registered, so nothing is walked, watched, or purged for it.
func (ing *Ingester) AddCodexSource() {
	root, err := config.CodexSessionsDir()
	if err != nil {
		ing.log.Warn("codex sessions dir unavailable", "err", err)
		return
	}
	if fi, statErr := os.Stat(root); statErr != nil || !fi.IsDir() {
		return
	}
	ing.AddSource(codexSource{root: root})
}
