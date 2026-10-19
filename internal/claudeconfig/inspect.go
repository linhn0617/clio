package claudeconfig

import "fmt"

// Read-only accessors for diagnostics (clio doctor). They reuse the same
// recognisers install/uninstall use, so doctor and install agree on what counts
// as "a clio hook".

// HookEntry is one clio hook entry in settings.json, as diagnostics see it.
type HookEntry struct {
	Binary string // the binary path the recogniser resolved
	// Unquoted marks the legacy shape: an absolute path with whitespace, unquoted,
	// which the shell splits, so the hook cannot run.
	Unquoted bool
	// OldMatcher marks a file-history entry under the matcher an older clio wrote
	// ("Read"), which install-hook upgrades to Read|Bash.
	OldMatcher bool
	// Uncovered is the matcher of a file-history entry whose group fires on neither
	// both Read and Bash nor the older "Read" shape: the user's own placement, which
	// install-hook leaves alone. Empty otherwise.
	Uncovered string
}

// ClioHookCommands returns every clio recall hook (SessionStart) and file-history
// hook (PreToolUse) in settingsPath regardless of matcher, each with the binary
// path the recogniser resolved (so a legacy unquoted path containing spaces comes
// back whole). A missing file yields empty results; a malformed one an error.
func ClioHookCommands(settingsPath string) (recall, fileHistory []HookEntry, err error) {
	root, err := load(settingsPath)
	if err != nil {
		return nil, nil, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	ss, _ := hooks["SessionStart"].([]any)
	pt, _ := hooks["PreToolUse"].([]any)
	return entriesMatching(ss, recallBinary, false), entriesMatching(pt, fileHistoryBinary, true), nil
}

// entriesMatching collects the hook entries resolve recognises. With fileHistory set
// it also judges each group's matcher with install-hook's rules: the older "Read"
// shape it upgrades, and a matcher that does not cover both Read and Bash.
func entriesMatching(groups []any, resolve func(string) (string, bool, bool), fileHistory bool) []HookEntry {
	var out []HookEntry
	for _, g := range groups {
		gm, hs, ok := groupHooks(g)
		if !ok {
			continue
		}
		old := fileHistory && isExactReadMatcher(gm["matcher"])
		uncovered := ""
		if fileHistory && !old && !groupCoversReadAndBash(gm) {
			uncovered = fmt.Sprint(gm["matcher"])
		}
		for _, h := range hs {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, ok := hm["command"].(string); ok {
				if bin, unquoted, ok := resolve(cmd); ok {
					out = append(out, HookEntry{Binary: bin, Unquoted: unquoted, OldMatcher: old, Uncovered: uncovered})
				}
			}
		}
	}
	return out
}

// ServerKind classifies a named mcpServers entry for diagnostics.
type ServerKind int

const (
	ServerAbsent    ServerKind = iota // no such entry (or no mcpServers / no config file)
	ServerLocal                       // has a command to run
	ServerRemote                      // an object without a command (sse/http: url-based)
	ServerMalformed                   // present but not a usable object (null, string, bad command)
)

// LookupServer reports how the named server is registered and, for a local one,
// its command. A missing config file means ServerAbsent; an unparsable file or a
// non-object mcpServers is an error.
func LookupServer(configPath, name string) (command string, kind ServerKind, err error) {
	root, err := load(configPath)
	if err != nil {
		return "", ServerAbsent, err
	}
	v, ok := root["mcpServers"]
	if !ok || v == nil {
		return "", ServerAbsent, nil
	}
	servers, ok := v.(map[string]any)
	if !ok {
		return "", ServerAbsent, fmt.Errorf("mcpServers in config is not a JSON object (found %T)", v)
	}
	raw, found := servers[name]
	if !found {
		return "", ServerAbsent, nil
	}
	entry, ok := raw.(map[string]any)
	if !ok {
		return "", ServerMalformed, nil
	}
	c, has := entry["command"]
	if !has {
		return "", ServerRemote, nil
	}
	if s, ok := c.(string); ok && s != "" {
		return s, ServerLocal, nil
	}
	return "", ServerMalformed, nil
}
