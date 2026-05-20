package ingest

import "strings"

// fallbackProjectPath best-effort reconstructs a path from Claude Code's encoded
// directory name. This is LOSSY: Claude Code replaces both '/' and '_' with '-',
// so the original cannot be recovered exactly. Used only when no event in a
// session carries a `cwd` field; the parser prefers the real cwd.
func fallbackProjectPath(encodedDir string) string {
	if encodedDir == "" {
		return ""
	}
	s := strings.TrimPrefix(encodedDir, "-")
	return "/" + strings.ReplaceAll(s, "-", "/")
}
