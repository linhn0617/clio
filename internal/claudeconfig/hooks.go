package claudeconfig

import (
	"fmt"
	"path/filepath"
	"strings"
)

// AddSessionStartHook registers a Claude Code SessionStart hook that runs the
// given command, preserving every existing hook and config key. Idempotent: if
// clio's recall hook is already present, it is a no-op.
func AddSessionStartHook(settingsPath, command string) error {
	return mutate(settingsPath, func(root map[string]any) error { return addSessionStartHook(root, command) })
}

// AddClioHooks registers the recall SessionStart hook and the file-history
// PreToolUse(Read) hook in ONE mutation, so the settings file's .bak is the
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
	if !sessionStartHasClio(ss) {
		ss = append(ss, map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": command},
			},
		})
	}
	hooks["SessionStart"] = ss
	root["hooks"] = hooks
	return nil
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
// with matcher "Read" and a 10-second timeout running the given command
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
	// Present only counts inside a matcher:"Read" group: an entry a user moved
	// under another matcher would never fire for Read, so we still add ours.
	if !groupsHaveWithMatcher(pt, "Read", isClioFileHistory) {
		pt = append(pt, map[string]any{
			"matcher": "Read",
			"hooks": []any{
				// Explicit short timeout: the reference default is 600 s and
				// the hook runs on every Read, so a stall must fail fast.
				map[string]any{"type": "command", "command": command, "timeout": 10},
			},
		})
	}
	hooks["PreToolUse"] = pt
	root["hooks"] = hooks
	return nil
}

// RemovePreToolUseReadHook removes clio's file-context hook entries, keeping any
// co-grouped foreign hooks and dropping the PreToolUse key only when empty.
func RemovePreToolUseReadHook(settingsPath string) error {
	return removeHookEntries(settingsPath, "PreToolUse", isClioFileHistory)
}

// HasPreToolUseReadHook reports whether clio's file-context hook is registered
// where it can fire: inside a matcher:"Read" group.
func HasPreToolUseReadHook(settingsPath string) (bool, error) {
	return hasHookEntry(settingsPath, "PreToolUse", "Read", isClioFileHistory)
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

// isClioFileHistory identifies the `<…/clio> file-history --hook` command with
// the same exact-shape rule as isClioRecall: any binary path, exact arguments.
func isClioFileHistory(command string) bool {
	f := strings.Fields(strings.TrimSpace(command))
	return len(f) == 3 && f[1] == "file-history" && f[2] == "--hook" && filepath.Base(f[0]) == "clio"
}

// isClioRecall identifies a `<…/clio> recall` hook command, regardless of the
// binary's absolute path so it stays removable across reinstalls, but tightly
// enough that unrelated commands (e.g. `clio-helper recall`) do not match.
func isClioRecall(command string) bool {
	f := strings.Fields(strings.TrimSpace(command))
	return len(f) == 2 && f[1] == "recall" && filepath.Base(f[0]) == "clio"
}
