package ingest

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// WalkSessionFiles returns all .jsonl files under projectsDir (recursive).
func WalkSessionFiles(projectsDir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(projectsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate unreadable entries
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
