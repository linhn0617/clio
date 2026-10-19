package claudeconfig

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/linhn0617/clio/internal/shellwords"
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

// FileHistoryInstall reports what installing the file-history hook actually did,
// so callers describe the real mutation instead of guessing beforehand.
type FileHistoryInstall struct {
	Added     bool   // no clio file-history entry existed; a Read|Bash group was added
	Upgraded  bool   // an older matcher:"Read" entry was retargeted or moved to Read|Bash
	Collapsed int    // duplicate clio file-history entries removed
	Misplaced string // matcher of a clio entry left alone because it does not cover both Read and Bash
}

// AddClioHooks registers the recall SessionStart hook and the file-history
// PreToolUse(Read|Bash) hook in ONE mutation, so the settings file's .bak is the
// pre-install content and the install is atomic. An empty fileHistoryCmd means
// the user opted out: any existing clio file-history hook is removed instead.
func AddClioHooks(settingsPath, recallCmd, fileHistoryCmd string) error {
	_, err := InstallClioHooks(settingsPath, recallCmd, fileHistoryCmd)
	return err
}

// InstallClioHooks is AddClioHooks that also reports what happened to the
// file-history hook (the zero value when it was opted out or already settled).
func InstallClioHooks(settingsPath, recallCmd, fileHistoryCmd string) (FileHistoryInstall, error) {
	var res FileHistoryInstall
	err := mutate(settingsPath, func(root map[string]any) error {
		if err := addSessionStartHook(root, recallCmd); err != nil {
			return err
		}
		if fileHistoryCmd == "" {
			return removeHookEntriesIn(root, "PreToolUse", isClioFileHistory)
		}
		var err error
		res, err = installFileHistory(root, fileHistoryCmd)
		return err
	})
	return res, err
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
	_, err := InstallPreToolUseReadHook(settingsPath, command)
	return err
}

// InstallPreToolUseReadHook is AddPreToolUseReadHook that reports what it did.
func InstallPreToolUseReadHook(settingsPath, command string) (FileHistoryInstall, error) {
	var res FileHistoryInstall
	err := mutate(settingsPath, func(root map[string]any) error {
		var err error
		res, err = installFileHistory(root, command)
		return err
	})
	return res, err
}

// installFileHistory makes exactly one clio file-history entry fire for both
// Read and Bash.
//   - A clio entry in a group whose matcher covers both counts as installed; the
//     first one met stays and later duplicates are removed (Collapsed).
//   - The shape an older clio wrote, matcher exactly "Read": a clio-only group is
//     retargeted in place (position and other fields kept, current command
//     applied, as upgradeLegacyRecall does); in a group shared with foreign hooks
//     only clio's entry moves to a new Read|Bash group (Upgraded).
//   - A clio entry under any other matcher is the user's placement: left alone,
//     reported in Misplaced, and no second group is added (it would fire twice on
//     the tool it already covers).
//   - With no clio entry at all, a Read|Bash group is added (Added).
func installFileHistory(root map[string]any, command string) (res FileHistoryInstall, err error) {
	hooks, err := objKey(root, "hooks")
	if err != nil {
		return res, err
	}
	pt, err := arrKey(hooks, "PreToolUse")
	if err != nil {
		return res, err
	}
	repairUnquoted(pt, " file-history --hook", command)
	isClio := func(h any) bool { return hookCommandMatches(h, isClioFileHistory) }
	existed := groupsHave(pt, isClioFileHistory)
	emptied := map[int]bool{}
	present, moved := false, false
	// Pass 1: groups that already cover Read and Bash — first clio entry wins.
	for i, g := range pt {
		gm, hs, ok := groupHooks(g)
		if !ok || !groupCoversReadAndBash(gm) {
			continue
		}
		kept := make([]any, 0, len(hs))
		for _, h := range hs {
			if isClio(h) {
				if present {
					res.Collapsed++
					continue
				}
				present = true
			}
			kept = append(kept, h)
		}
		gm["hooks"] = kept
		emptied[i] = len(hs) > 0 && len(kept) == 0
	}
	// Pass 2: exact "Read" groups (the older shape).
	for i, g := range pt {
		gm, hs, ok := groupHooks(g)
		if !ok || !isExactReadMatcher(gm["matcher"]) {
			continue
		}
		var foreign, clio []any
		for _, h := range hs {
			if isClio(h) {
				clio = append(clio, h)
			} else {
				foreign = append(foreign, h)
			}
		}
		switch {
		case len(clio) == 0:
		case len(foreign) == 0 && !present:
			first := clio[0].(map[string]any)
			first["command"] = command
			if first["timeout"] == nil {
				first["timeout"] = 10
			}
			gm["matcher"] = fileHistoryMatcher
			gm["hooks"] = []any{first}
			present = true
			res.Upgraded = true
			res.Collapsed += len(clio) - 1
		default:
			gm["hooks"] = foreign
			emptied[i] = len(foreign) == 0
			if present {
				res.Collapsed += len(clio)
			} else {
				res.Collapsed += len(clio) - 1
				moved = true // the first entry moves to a new group below
			}
		}
	}
	out := make([]any, 0, len(pt)+1)
	for i, g := range pt {
		if !emptied[i] {
			out = append(out, g)
		}
	}
	switch {
	case present:
	case moved:
		res.Upgraded = true
	case existed:
		for _, g := range out {
			if gm, hs, ok := groupHooks(g); ok && groupsHave([]any{map[string]any{"hooks": hs}}, isClioFileHistory) {
				res.Misplaced = fmt.Sprint(gm["matcher"])
				break
			}
		}
	default:
		res.Added = true
	}
	if !present && (moved || !existed) {
		out = append(out, map[string]any{
			"matcher": fileHistoryMatcher,
			"hooks": []any{
				// Explicit short timeout: the reference default is 600 s and
				// the hook runs on every Read and Bash, so a stall must fail fast.
				map[string]any{"type": "command", "command": command, "timeout": 10},
			},
		})
	}
	hooks["PreToolUse"] = out
	root["hooks"] = hooks
	return res, nil
}

func groupCoversReadAndBash(gm map[string]any) bool {
	m := gm["matcher"]
	return matcherCoversTool(m, "Read") && matcherCoversTool(m, "Bash")
}

// exactListRe is Claude Code's "exact string / list" matcher alphabet: letters,
// digits, `_`, `-`, spaces, `,` and `|`.
var exactListRe = regexp.MustCompile(`^[A-Za-z0-9_\- ,|]+$`)

// matcherList splits an exact-list matcher on `|` and `,`, trimming each item.
func matcherList(m string) []string {
	items := strings.FieldsFunc(m, func(r rune) bool { return r == '|' || r == ',' })
	for i := range items {
		items[i] = strings.TrimSpace(items[i])
	}
	return items
}

// matcherCoversTool reports whether a hook matcher fires for the named tool,
// following Claude Code's hooks documentation (Matcher patterns): "*", "" or an
// absent matcher match everything; a matcher made only of letters, digits, `_`,
// `-`, spaces, `,` and `|` is a list of exact names; anything else is a
// JavaScript regex tested unanchored. Go's regexp stands in for the JS engine
// (equivalent on the simple patterns in real configs); a pattern that does not
// compile, or a non-string matcher, is treated as not covering.
func matcherCoversTool(matcher any, tool string) bool {
	if matcher == nil {
		return true
	}
	m, ok := matcher.(string)
	if !ok {
		return false
	}
	if m == "" || m == "*" {
		return true
	}
	if exactListRe.MatchString(m) {
		for _, item := range matcherList(m) {
			if item == tool {
				return true
			}
		}
		return false
	}
	re, err := regexp.Compile(m)
	return err == nil && re.MatchString(tool)
}

// isExactReadMatcher reports the shape an older clio wrote: an exact-list matcher
// whose every item is "Read" (so `Read` and ` Read `, but not `Read|Bash`).
func isExactReadMatcher(matcher any) bool {
	m, ok := matcher.(string)
	if !ok || !exactListRe.MatchString(m) {
		return false
	}
	items := matcherList(m)
	for _, it := range items {
		if it != readMatcher {
			return false
		}
	}
	return len(items) > 0
}

// groupHooks returns a group's hooks array, or nil/false for a malformed group.
func groupHooks(g any) (gm map[string]any, hs []any, ok bool) {
	gm, ok = g.(map[string]any)
	if !ok {
		return nil, nil, false
	}
	hs, ok = gm["hooks"].([]any)
	return gm, hs, ok
}

// RemovePreToolUseReadHook removes clio's file-context hook entries, keeping any
// co-grouped foreign hooks and dropping the PreToolUse key only when empty.
func RemovePreToolUseReadHook(settingsPath string) error {
	return removeHookEntries(settingsPath, "PreToolUse", isClioFileHistory)
}

// HasPreToolUseReadHook reports whether clio's file-context hook is fully
// registered: a clio entry sits in a group whose matcher covers both Read and
// Bash. An older Read-only entry is not (install-hook upgrades it).
func HasPreToolUseReadHook(settingsPath string) (bool, error) {
	root, err := load(settingsPath)
	if err != nil {
		return false, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	pt, _ := hooks["PreToolUse"].([]any)
	for _, g := range pt {
		if gm, hs, ok := groupHooks(g); ok && groupCoversReadAndBash(gm) {
			for _, h := range hs {
				if hookCommandMatches(h, isClioFileHistory) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// HasAnyClioHookEntries reports whether any clio hook entry exists at all,
// regardless of matcher — what uninstall needs, since a misplaced entry still
// deserves removal even though it would never fire.
func HasAnyClioHookEntries(settingsPath string) (recall, fileHistory bool, err error) {
	recall, err = hasHookEntry(settingsPath, "SessionStart", isClioRecall)
	if err != nil {
		return false, false, err
	}
	fileHistory, err = hasHookEntry(settingsPath, "PreToolUse", isClioFileHistory)
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

// hasHookEntry reports whether any entry under event has a matching command.
func hasHookEntry(settingsPath, event string, match func(string) bool) (bool, error) {
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
	return groupsHave(groups, match), nil
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

// isClioName reports whether a binary path's base name is clio (clio.exe on Windows).
func isClioName(path string) bool {
	base := filepath.Base(path)
	return base == "clio" || base == "clio.exe"
}

// clioBinaryOf returns the binary path of a `<…/clio> args…` command: the exact
// argument shape is required, the binary may live anywhere. The legacy unquoted
// shape (a path with spaces) is resolved to the whole path and reported as
// unquoted, since the shell splits it and that hook never runs.
func clioBinaryOf(command, suffix string, args ...string) (bin string, unquoted, ok bool) {
	f, split := shellwords.Split(command)
	if split && len(f) == 1+len(args) && isClioName(f[0]) {
		match := true
		for i, a := range args {
			if f[1+i] != a {
				match = false
				break
			}
		}
		if match {
			return f[0], false, true
		}
	}
	if isUnquotedClio(command, suffix) {
		return strings.TrimRight(strings.TrimSuffix(command, suffix), " \t"), true, true
	}
	return "", false, false
}

func fileHistoryBinary(command string) (string, bool, bool) {
	return clioBinaryOf(command, " file-history --hook", "file-history", "--hook")
}

func recallBinary(command string) (string, bool, bool) {
	return clioBinaryOf(command, " recall", "recall")
}

// isClioFileHistory identifies the `<…/clio> file-history --hook` command with
// the same exact-shape rule as isClioRecall: any binary path, exact arguments.
func isClioFileHistory(command string) bool {
	_, _, ok := fileHistoryBinary(command)
	return ok
}

// isClioRecall identifies a `<…/clio> recall` hook command, regardless of the
// binary's absolute path so it stays removable across reinstalls, but tightly
// enough that unrelated commands (e.g. `clio-helper recall`) do not match.
func isClioRecall(command string) bool {
	_, _, ok := recallBinary(command)
	return ok
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
		strings.ContainsAny(exe, "\n\r'\"\\$`&|;<>()*?[]{}!#~") || !isClioName(exe) {
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
