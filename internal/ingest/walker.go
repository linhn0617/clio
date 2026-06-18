package ingest

import (
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
)

// WalkSessionFiles returns all .jsonl files under projectsDir (recursive).
func WalkSessionFiles(projectsDir string, log *slog.Logger) ([]string, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	var out []string
	err := filepath.WalkDir(projectsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Warn("skip unreadable entry", "path", path, "err", err)
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".jsonl") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// sessionUUIDFromPath derives the session UUID from the file name (Claude Code
// names each session file <uuid>.jsonl). Used as a stable key before the file's
// events are parsed.
func sessionUUIDFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".jsonl")
}

// parentDirName returns the encoded project directory name for fallback path
// reconstruction.
func parentDirName(path string) string {
	return filepath.Base(filepath.Dir(path))
}

// isSubagentFile reports whether path is a Claude Code subagent transcript: a file
// directly inside a "subagents" directory
// (<project>/<parent-session-uuid>/subagents/agent-<id>.jsonl).
func isSubagentFile(path string) bool {
	return filepath.Base(filepath.Dir(path)) == "subagents"
}

// subagentParentDir returns the parent-session-uuid directory that holds a subagent
// file's "subagents" dir — the fallback parent link when the transcript's lines
// carry no sessionId.
func subagentParentDir(path string) string {
	return filepath.Base(filepath.Dir(filepath.Dir(path)))
}

// subagentProjectDirName returns the encoded project directory above a subagent
// file's <parent-uuid>/subagents/ path, for project-path fallback when the
// transcript carries no cwd (parentDirName would yield the literal "subagents").
func subagentProjectDirName(path string) string {
	return filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path))))
}
