package claudeconfig

// Read-only accessors for diagnostics (clio doctor). They reuse the same
// recognisers install/uninstall use, so doctor and install agree on what counts
// as "a clio hook".

// ClioHookCommands returns the commands of every clio recall hook (SessionStart)
// and file-history hook (PreToolUse) in settingsPath, regardless of matcher. A
// missing file yields empty results; a malformed one an error.
func ClioHookCommands(settingsPath string) (recall, fileHistory []string, err error) {
	root, err := load(settingsPath)
	if err != nil {
		return nil, nil, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	ss, _ := hooks["SessionStart"].([]any)
	pt, _ := hooks["PreToolUse"].([]any)
	return commandsMatching(ss, isClioRecall), commandsMatching(pt, isClioFileHistory), nil
}

func commandsMatching(groups []any, match func(string) bool) []string {
	var out []string
	for _, g := range groups {
		_, hs, ok := groupHooks(g)
		if !ok {
			continue
		}
		for _, h := range hs {
			if hookCommandMatches(h, match) {
				out = append(out, h.(map[string]any)["command"].(string))
			}
		}
	}
	return out
}

// ServerCommand returns the registered command of the named mcpServers entry.
// ok is false when the config is absent or has no such server.
func ServerCommand(configPath, name string) (command string, ok bool, err error) {
	root, err := load(configPath)
	if err != nil {
		return "", false, err
	}
	servers, err := serversMap(root)
	if err != nil {
		return "", false, err
	}
	entry, found := servers[name].(map[string]any)
	if !found {
		return "", false, nil
	}
	command, _ = entry["command"].(string)
	return command, true, nil
}
