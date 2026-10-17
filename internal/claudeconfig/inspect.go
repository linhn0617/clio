package claudeconfig

import "fmt"

// Read-only accessors for diagnostics (clio doctor). They reuse the same
// recognisers install/uninstall use, so doctor and install agree on what counts
// as "a clio hook".

// ClioHookCommands returns, for every clio recall hook (SessionStart) and
// file-history hook (PreToolUse) in settingsPath regardless of matcher, the binary
// path the recogniser resolved (so a legacy unquoted path containing spaces comes
// back whole). A missing file yields empty results; a malformed one an error.
func ClioHookCommands(settingsPath string) (recall, fileHistory []string, err error) {
	root, err := load(settingsPath)
	if err != nil {
		return nil, nil, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	ss, _ := hooks["SessionStart"].([]any)
	pt, _ := hooks["PreToolUse"].([]any)
	return binariesMatching(ss, recallBinary), binariesMatching(pt, fileHistoryBinary), nil
}

func binariesMatching(groups []any, resolve func(string) (string, bool)) []string {
	var out []string
	for _, g := range groups {
		_, hs, ok := groupHooks(g)
		if !ok {
			continue
		}
		for _, h := range hs {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, ok := hm["command"].(string); ok {
				if bin, ok := resolve(cmd); ok {
					out = append(out, bin)
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
