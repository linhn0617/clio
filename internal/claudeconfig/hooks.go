package claudeconfig

import (
	"fmt"
	"path/filepath"
	"strings"
)

// recallMatcher is every SessionStart source except `fork`: a forked session
// already carries its parent's context, so a digest would only repeat it.
const recallMatcher = "startup|resume|clear|compact"

// fileHistoryMatcher: the file-history hook also fires on Bash, where it stays
// silent unless the command names an existing file under cwd. Older clio wrote
// readMatcher alone.
const (
	fileHistoryMatcher = "Read|Bash"
	readMatcher        = "Read"
)

// recallTimeout (seconds) replaces Claude Code's 600 s default: a cold-cache
// read of a large index must not hold session startup hostage.
const recallTimeout = 10

// AddSessionStartHook registers a Claude Code SessionStart hook that runs the
// given command (matcher recallMatcher, timeout recallTimeout), preserving every
// existing hook and config key. Idempotent: if clio's recall hook is already
// present it is a no-op, except that an entry in the pre-matcher shape an older
// clio wrote is upgraded in place rather than duplicated.
func AddSessionStartHook(settingsPath, command string) error {
	return mutate(settingsPath, func(root map[string]any) error { return addSessionStartHook(root, command) })
}

// AddClioHooks registers the recall SessionStart hook and the file-history
// PreToolUse(Read|Bash) hook in ONE mutation, so the settings file's .bak is the
// pre-install content and the install is atomic. An empty fileHistoryCmd means
// the user opted out: any existing clio file-history hook is removed instead.
func AddClioHooks(settingsPath, recallCmd, fileHistoryCmd string) error {
	return mutate(settingsPath, func(root map[string]any) error {
		if err := addSessionStartHook(root, recallCmd); err != nil {
			return err
		}
		if fileHistoryCmd == "" {
			return removeHookEntriesIn(root, "PreToolUse", isClioFileHistory)
		}
		return addPreToolUseReadHook(root, fileHistoryCmd)
	})
}

// RemoveClioHooks removes both of clio's hooks in one mutation, keeping foreign
// hooks (co-grouped ones included) and dropping emptied event keys.
func RemoveClioHooks(settingsPath string) error {
	return mutate(settingsPath, func(root map[string]any) error {
		if err := removeHookEntriesIn(root, "SessionStart", isClioRecall); err != nil {
			return err
		}
		if err := removeHookEntriesIn(root, "PreToolUse", isClioFileHistory); err != nil {
			return err
		}
		if hooks, ok := root["hooks"].(map[string]any); ok && len(hooks) == 0 {
			delete(root, "hooks") // leave no empty object behind
		}
		return nil
	})
}

func addSessionStartHook(root map[string]any, command string) error {
	hooks, err := objKey(root, "hooks")
	if err != nil {
		return err
	}
	ss, err := arrKey(hooks, "SessionStart")
	if err != nil {
		return err
	}
	upgradeLegacyRecall(ss, command)
	repairUnquoted(ss, " recall", command)
	if !sessionStartHasClio(ss) {
		ss = append(ss, map[string]any{
			"matcher": recallMatcher,
			"hooks": []any{
				map[string]any{"type": "command", "command": command, "timeout": recallTimeout},
			},
		})
	}
	hooks["SessionStart"] = ss
	root["hooks"] = hooks
	return nil
}

// isUnmatchedGroup reports whether a group has no matcher (absent or ""), the
// shape older clio versions wrote; a user-chosen matcher is left alone.
func isUnmatchedGroup(gm map[string]any) bool {
	m, has := gm["matcher"]
	return !has || m == ""
}

// upgradeLegacyRecall rewrites clio recall entries in unmatched groups: a group
// holding only clio entries gains the matcher (and the timeout, unless one is
// set) in place; from a group shared with foreign hooks the clio entries are
// removed so the caller appends a matched group, leaving the foreign hooks
// firing exactly as before. An upgraded entry gets the current command: the old
// one may name a binary that has since moved.
func upgradeLegacyRecall(ss []any, command string) {
	for _, g := range ss {
		gm, ok := g.(map[string]any)
		if !ok || !isUnmatchedGroup(gm) {
			continue
		}
		hs, ok := gm["hooks"].([]any)
		if !ok {
			continue
		}
		var foreign []any
		clio := 0
		for _, h := range hs {
			if hookIsClioRecall(h) {
				clio++
			} else {
				foreign = append(foreign, h)
			}
		}
		switch {
		case clio == 0:
		case len(foreign) == 0:
			gm["matcher"] = recallMatcher
			for _, h := range hs {
				hm := h.(map[string]any)
				hm["command"] = command
				if hm["timeout"] == nil {
					hm["timeout"] = recallTimeout
				}
			}
		default:
			gm["hooks"] = foreign
		}
	}
}

// RemoveSessionStartHook removes clio's recall SessionStart hook, leaving any
// other SessionStart hooks (and the rest of the config) intact.
func RemoveSessionStartHook(settingsPath string) error {
	return mutate(settingsPath, func(root map[string]any) error {
		hooks, err := objKey(root, "hooks")
		if err != nil {
			return err
		}
		ss, err := arrKey(hooks, "SessionStart")
		if err != nil {
			return err
		}
		kept := make([]any, 0, len(ss))
		for _, g := range ss {
			gm, ok := g.(map[string]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			hs, ok := gm["hooks"].([]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			// Remove only clio's hook entries, preserving any co-grouped hooks.
			keptHooks := make([]any, 0, len(hs))
			for _, h := range hs {
				if !hookIsClioRecall(h) {
					keptHooks = append(keptHooks, h)
				}
			}
			if len(keptHooks) == 0 {
				continue // the group held only clio's hook(s); drop the now-empty group
			}
			gm["hooks"] = keptHooks
			kept = append(kept, gm)
		}
		if len(kept) == 0 {
			delete(hooks, "SessionStart")
		} else {
			hooks["SessionStart"] = kept
		}
		root["hooks"] = hooks
		return nil
	})
}

// HasSessionStartHook reports whether clio's recall hook is registered.
func HasSessionStartHook(settingsPath string) (bool, error) {
	root, err := load(settingsPath)
	if err != nil {
		return false, err
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return false, nil
	}
	ss, ok := hooks["SessionStart"].([]any)
	if !ok {
		return false, nil
	}
	return sessionStartHasClio(ss), nil
}

// HasLegacySessionStartHook reports whether a clio recall entry sits in an
// unmatched SessionStart group — the shape install-hook upgrades.
func HasLegacySessionStartHook(settingsPath string) (bool, error) {
	root, err := load(settingsPath)
	if err != nil {
		return false, err
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return false, nil
	}
	ss, ok := hooks["SessionStart"].([]any)
	if !ok {
		return false, nil
	}
	for _, g := range ss {
		if gm, ok := g.(map[string]any); ok && isUnmatchedGroup(gm) && groupIsClioRecall(gm) {
			return true, nil
		}
	}
	return false, nil
}

// HasUnquotedClioHook reports whether a clio hook entry has the broken unquoted
// space-path shape that install-hook repairs.
func HasUnquotedClioHook(settingsPath string) (bool, error) {
	root, err := load(settingsPath)
	if err != nil {
		return false, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	for event, suffix := range map[string]string{"SessionStart": " recall", "PreToolUse": " file-history --hook"} {
		groups, _ := hooks[event].([]any)
		if groupsHave(groups, func(c string) bool { return isUnquotedClio(c, suffix) }) {
			return true, nil
		}
	}
	return false, nil
}

// objKey returns root[key] as an object, creating an empty one if absent/null,
// and refusing if it holds meaningful non-object data.
func objKey(m map[string]any, key string) (map[string]any, error) {
	v, ok := m[key]
	if !ok || v == nil {
		nm := map[string]any{}
		m[key] = nm
		return nm, nil
	}
	o, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s in config is not a JSON object (found %T); refusing to modify", key, v)
	}
	return o, nil
}

// arrKey returns m[key] as an array, empty if absent/null, refusing non-array data.
func arrKey(m map[string]any, key string) ([]any, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return []any{}, nil
	}
	a, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s in config is not a JSON array (found %T); refusing to modify", key, v)
	}
	return a, nil
}

func sessionStartHasClio(ss []any) bool {
	for _, g := range ss {
		if groupIsClioRecall(g) {
			return true
		}
	}
	return false
}

// groupIsClioRecall reports whether a SessionStart group contains clio's recall hook.
func groupIsClioRecall(group any) bool {
	gm, ok := group.(map[string]any)
	if !ok {
		return false
	}
	hs, ok := gm["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range hs {
		if hookIsClioRecall(h) {
			return true
		}
	}
	return false
}

// hookIsClioRecall reports whether one hook entry is clio's recall hook.
func hookIsClioRecall(h any) bool {
	hm, ok := h.(map[string]any)
	if !ok {
		return false
	}
	cmd, ok := hm["command"].(string)
	return ok && isClioRecall(cmd)
}

// AddPreToolUseReadHook registers clio's file-context hook: a PreToolUse group
// with matcher "Read|Bash" and a 10-second timeout running the given command
// (`<exe> file-history --hook`). Idempotent; other groups and keys untouched.
func AddPreToolUseReadHook(settingsPath, command string) error {
	return mutate(settingsPath, func(root map[string]any) error { return addPreToolUseReadHook(root, command) })
}

func addPreToolUseReadHook(root map[string]any, command string) error {
	hooks, err := objKey(root, "hooks")
	if err != nil {
		return err
	}
	pt, err := arrKey(hooks, "PreToolUse")
	if err != nil {
		return err
	}
	repairUnquoted(pt, " file-history --hook", command)
	var present bool
	pt, present = upgradeReadFileHistory(pt, command)
	// Present only counts inside a Read|Bash group: an entry a user moved under
	// another matcher would never fire for Read, so we still add ours.
	if !present {
		pt = append(pt, map[string]any{
			"matcher": fileHistoryMatcher,
			"hooks": []any{
				// Explicit short timeout: the reference default is 600 s and
				// the hook runs on every Read and Bash, so a stall must fail fast.
				map[string]any{"type": "command", "command": command, "timeout": 10},
			},
		})
	}
	hooks["PreToolUse"] = pt
	root["hooks"] = hooks
	return nil
}

// upgradeReadFileHistory moves clio's entries out of matcher:"Read" groups, the
// shape an older clio wrote. A clio-only group is retargeted in place (and takes
// the current command, as upgradeLegacyRecall does); in a group shared with
// foreign hooks only clio's entry is removed so the foreign hooks keep firing
// exactly as before. If a Read|Bash clio entry already exists, leftovers are
// just dropped. present reports whether a Read|Bash clio entry exists after.
func upgradeReadFileHistory(pt []any, command string) (out []any, present bool) {
	present = groupsHaveWithMatcher(pt, fileHistoryMatcher, isClioFileHistory)
	out = make([]any, 0, len(pt))
	for _, g := range pt {
		gm, ok := g.(map[string]any)
		hs, hok := gm["hooks"].([]any)
		if !ok || !hok || gm["matcher"] != readMatcher {
			out = append(out, g)
			continue
		}
		var foreign []any
		for _, h := range hs {
			if !hookCommandMatches(h, isClioFileHistory) {
				foreign = append(foreign, h)
			}
		}
		switch {
		case len(foreign) == len(hs): // no clio entry here
			out = append(out, g)
		case len(foreign) == 0 && !present:
			gm["matcher"] = fileHistoryMatcher
			for _, h := range hs {
				if hm, ok := h.(map[string]any); ok {
					hm["command"] = command
					if hm["timeout"] == nil {
						hm["timeout"] = 10
					}
				}
			}
			present = true
			out = append(out, g)
		case len(foreign) == 0: // clio-only duplicate of an existing Read|Bash entry
		default:
			gm["hooks"] = foreign
			out = append(out, g)
		}
	}
	return out, present
}

// RemovePreToolUseReadHook removes clio's file-context hook entries, keeping any
// co-grouped foreign hooks and dropping the PreToolUse key only when empty.
func RemovePreToolUseReadHook(settingsPath string) error {
	return removeHookEntries(settingsPath, "PreToolUse", isClioFileHistory)
}

// HasPreToolUseReadHook reports whether clio's file-context hook is registered
// where it can fire for Read: inside a matcher:"Read|Bash" group, or the
// matcher:"Read" group an older clio wrote (install-hook upgrades that one).
func HasPreToolUseReadHook(settingsPath string) (bool, error) {
	if has, err := hasHookEntry(settingsPath, "PreToolUse", fileHistoryMatcher, isClioFileHistory); has || err != nil {
		return has, err
	}
	return hasHookEntry(settingsPath, "PreToolUse", readMatcher, isClioFileHistory)
}

// HasLegacyPreToolUseReadHook reports whether clio's entry still sits in a
// matcher:"Read" group — the shape install-hook upgrades to Read|Bash.
func HasLegacyPreToolUseReadHook(settingsPath string) (bool, error) {
	return hasHookEntry(settingsPath, "PreToolUse", readMatcher, isClioFileHistory)
}

// HasAnyClioHookEntries reports whether any clio hook entry exists at all,
// regardless of matcher — what uninstall needs, since a misplaced entry still
// deserves removal even though it would never fire.
func HasAnyClioHookEntries(settingsPath string) (recall, fileHistory bool, err error) {
	recall, err = hasHookEntry(settingsPath, "SessionStart", "", isClioRecall)
	if err != nil {
		return false, false, err
	}
	fileHistory, err = hasHookEntry(settingsPath, "PreToolUse", "", isClioFileHistory)
	return recall, fileHistory, err
}

// removeHookEntries drops every hook entry under event whose command matches,
// preserving co-grouped foreign entries and deleting an emptied event key.
func removeHookEntries(settingsPath, event string, match func(string) bool) error {
	return mutate(settingsPath, func(root map[string]any) error { return removeHookEntriesIn(root, event, match) })
}

func removeHookEntriesIn(root map[string]any, event string, match func(string) bool) error {
	hooks, err := objKey(root, "hooks")
	if err != nil {
		return err
	}
	groups, err := arrKey(hooks, event)
	if err != nil {
		return err
	}
	kept := make([]any, 0, len(groups))
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			kept = append(kept, g)
			continue
		}
		hs, ok := gm["hooks"].([]any)
		if !ok {
			kept = append(kept, g)
			continue
		}
		keptHooks := make([]any, 0, len(hs))
		for _, h := range hs {
			if !hookCommandMatches(h, match) {
				keptHooks = append(keptHooks, h)
			}
		}
		if len(keptHooks) == 0 {
			continue // the group held only clio's entries; drop it
		}
		gm["hooks"] = keptHooks
		kept = append(kept, gm)
	}
	if len(kept) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = kept
	}
	root["hooks"] = hooks
	return nil
}

// hasHookEntry reports whether any entry under event has a matching command;
// a non-empty matcher restricts the search to groups with exactly that matcher.
func hasHookEntry(settingsPath, event, matcher string, match func(string) bool) (bool, error) {
	root, err := load(settingsPath)
	if err != nil {
		return false, err
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return false, nil
	}
	groups, ok := hooks[event].([]any)
	if !ok {
		return false, nil
	}
	if matcher != "" {
		return groupsHaveWithMatcher(groups, matcher, match), nil
	}
	return groupsHave(groups, match), nil
}

// groupsHaveWithMatcher is groupsHave restricted to groups whose matcher is
// exactly matcher.
func groupsHaveWithMatcher(groups []any, matcher string, match func(string) bool) bool {
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok || gm["matcher"] != matcher {
			continue
		}
		if groupsHave([]any{gm}, match) {
			return true
		}
	}
	return false
}

func groupsHave(groups []any, match func(string) bool) bool {
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		hs, ok := gm["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hs {
			if hookCommandMatches(h, match) {
				return true
			}
		}
	}
	return false
}

func hookCommandMatches(h any, match func(string) bool) bool {
	hm, ok := h.(map[string]any)
	if !ok {
		return false
	}
	cmd, ok := hm["command"].(string)
	return ok && match(cmd)
}

// ShellQuote makes path safe to embed as the first word of a hook command
// (Claude Code runs it through a shell). A path with no whitespace or shell
// metacharacter is returned unchanged so existing installs stay byte-identical;
// otherwise POSIX single quotes, with an embedded ' written as '\”.
func ShellQuote(path string) string {
	if path != "" && !strings.ContainsAny(path, " \t\n\r'\"\\$`&|;<>()*?[]{}!#~") {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// splitCommand is the minimal word splitter the recognisers need: whitespace
// separated words, '…' literal, "…" with backslash escapes, and a bare \x
// escape (which is what ShellQuote emits between '…' runs). ok is false for an
// unterminated quote or trailing backslash, which no shell would accept either.
func splitCommand(cmd string) (words []string, ok bool) {
	var cur strings.Builder
	inWord := false
	rs := []rune(cmd)
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\'':
			inWord = true
			i++
			for ; i < len(rs) && rs[i] != '\''; i++ {
				cur.WriteRune(rs[i])
			}
			if i == len(rs) {
				return nil, false
			}
		case c == '"':
			inWord = true
			i++
			for ; i < len(rs) && rs[i] != '"'; i++ {
				if rs[i] == '\\' && i+1 < len(rs) && strings.ContainsRune("\"\\$`", rs[i+1]) {
					i++
				}
				cur.WriteRune(rs[i])
			}
			if i == len(rs) {
				return nil, false
			}
		case c == '\\':
			if i+1 == len(rs) {
				return nil, false
			}
			inWord = true
			i++
			cur.WriteRune(rs[i])
		default:
			inWord = true
			cur.WriteRune(c)
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, true
}

// isClioFileHistory identifies the `<…/clio> file-history --hook` command with
// the same exact-shape rule as isClioRecall: any binary path, exact arguments.
func isClioFileHistory(command string) bool {
	f, ok := splitCommand(command)
	if ok && len(f) == 3 && f[1] == "file-history" && f[2] == "--hook" && filepath.Base(f[0]) == "clio" {
		return true
	}
	return isUnquotedClio(command, " file-history --hook")
}

// isClioRecall identifies a `<…/clio> recall` hook command, regardless of the
// binary's absolute path so it stays removable across reinstalls, but tightly
// enough that unrelated commands (e.g. `clio-helper recall`) do not match.
func isClioRecall(command string) bool {
	f, ok := splitCommand(command)
	if ok && len(f) == 2 && f[1] == "recall" && filepath.Base(f[0]) == "clio" {
		return true
	}
	return isUnquotedClio(command, " recall")
}

// isUnquotedClio recognises the shape clio wrote before it quoted the binary path:
// an absolute path containing whitespace, unquoted, so the shell splits it and the
// hook never ran. It must stay recognisable so install can repair it and uninstall
// can remove it. Only a bare absolute path qualifies (no quote or shell
// metacharacter), so a foreign command that merely ends in `/clio recall` is not
// taken for ours. A wrapper such as `/usr/bin/env /opt/clio recall` is told apart
// heuristically: a split path's later pieces continue a directory name, while a
// wrapper's later argument is a path of its own, so any piece after the first that
// starts with /, ~, ./ or ../ (or is . or ..) marks a foreign command. The limits: a real path with a
// space followed by / (`/a /b/clio`) is not recognised, and a wrapper whose
// argument is relative without ./ or ../ (`/usr/bin/env bin/clio recall`) is
// still taken for clio's; `./` and `../` pieces are told apart, at the cost of a
// real directory name ending in ` .` (`/a/My ./clio`) no longer being recognised.
func isUnquotedClio(command, suffix string) bool {
	if !strings.HasSuffix(command, suffix) {
		return false
	}
	exe := strings.TrimRight(strings.TrimSuffix(command, suffix), " \t")
	if !strings.HasPrefix(exe, "/") || !strings.ContainsAny(exe, " \t") ||
		strings.ContainsAny(exe, "\n\r'\"\\$`&|;<>()*?[]{}!#~") || filepath.Base(exe) != "clio" {
		return false
	}
	for _, f := range strings.Fields(exe)[1:] {
		if strings.HasPrefix(f, "/") || strings.HasPrefix(f, "~") ||
			strings.HasPrefix(f, "./") || strings.HasPrefix(f, "../") || f == "." || f == ".." {
			return false
		}
	}
	return true
}

// repairUnquoted rewrites every broken unquoted clio entry matching suffix to
// command (the current, quoted binary path).
func repairUnquoted(groups []any, suffix, command string) {
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			if hm, ok := h.(map[string]any); ok {
				if c, ok := hm["command"].(string); ok && isUnquotedClio(c, suffix) {
					hm["command"] = command
				}
			}
		}
	}
}
