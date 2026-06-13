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
	return mutate(settingsPath, func(root map[string]any) error {
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
	})
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

// isClioRecall identifies a `<…/clio> recall` hook command, regardless of the
// binary's absolute path so it stays removable across reinstalls, but tightly
// enough that unrelated commands (e.g. `clio-helper recall`) do not match.
func isClioRecall(command string) bool {
	f := strings.Fields(strings.TrimSpace(command))
	return len(f) == 2 && f[1] == "recall" && filepath.Base(f[0]) == "clio"
}
