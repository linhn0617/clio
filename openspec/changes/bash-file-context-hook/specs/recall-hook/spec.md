## MODIFIED Requirements

### Requirement: Opt-in SessionStart hook installation

`clio install-hook` SHALL register a Claude Code SessionStart hook (in
`~/.claude/settings.json`) that runs `clio recall` with matcher
`startup|resume|clear|compact` (not `fork`, whose session already carries its
parent's context) and a 10-second timeout, and — unless `--no-file-context` is
given — a PreToolUse hook with matcher `Read|Bash` and a 10-second timeout that runs
`clio file-history --hook`; `clio uninstall-hook` SHALL remove both. Installation SHALL
be atomic (leaving the original config intact on failure), SHALL preserve any
existing hooks in either event (including foreign hooks that share a group with
clio's), and SHALL be idempotent per hook (re-running adds a missing hook
without duplicating a present one). A recall entry in a group without a matcher
(the shape older clio versions wrote) SHALL be upgraded in place to the matcher and
timeout above rather than duplicated; when that group also holds foreign hooks, the
clio entry SHALL move to its own matched group and the foreign hooks SHALL stay
where they were. A recall entry the user placed under a different matcher SHALL be
left unchanged. A file-history entry counts as installed when it sits in a group whose
matcher covers both `Read` and `Bash`, where a matcher "covers" a tool per Claude Code's
matcher rules: an absent, empty or `*` matcher matches every tool; a matcher made only of
letters, digits, `_`, `-`, spaces, `,` and `|` is a list of exact tool names (items separated
by `|` or `,`, surrounding whitespace ignored); any other matcher is a regular expression
tested unanchored against the tool name (an uncompilable pattern, or a non-string matcher,
covers nothing). An entry an older clio wrote under matcher `Read` (an exact list holding only
`Read`) SHALL be upgraded: a group holding only clio's entry is retargeted in place to
`Read|Bash` (position and other fields kept, current command applied), while from a group
shared with foreign hooks clio's entry SHALL move to its own `Read|Bash` group and the
foreign hooks SHALL stay under `Read`. A clio entry under any other matcher (for example
`Read|Edit`, `Bash`, `^Read$`, `Edit`) is the user's placement: it SHALL be left unchanged,
no second group SHALL be added, and `install-hook` SHALL print a warning naming the matcher
and saying it does not cover both Read and Bash. With no clio file-history entry at all, a
`Read|Bash` group SHALL be added. Within one install, duplicate clio file-history entries in
groups covering both tools SHALL be reduced to the first one met (groups left empty are
removed, foreign hooks stay). `install-hook` SHALL describe what it actually changed:
registered, upgraded (only when a matcher was retargeted or an entry moved out of a shared
`Read` group), collapsed N duplicates, or "nothing to add" only when the file was not
modified. It is opt-in and SHALL
NOT be performed by `install-mcp`. clio's hook entries SHALL be recognised by
their exact command shape (`<…/clio> recall`, `<…/clio> file-history --hook`)
regardless of the binary's absolute path and of the group's matcher, so removal
handles both the old and the new shape. The binary path SHALL be shell-quoted in the
command when it contains whitespace or a shell metacharacter (POSIX single quotes; a
plain path stays unquoted, so existing installs are unchanged), and the recognisers
SHALL accept the first word either bare or single/double-quoted. An entry an older
clio wrote with an unquoted absolute path containing whitespace (which the shell
splits, so it never ran) SHALL also be recognised: `install-hook` SHALL rewrite it in
place to the quoted command and `uninstall-hook` SHALL remove it. Any entry upgraded
in place SHALL get the current binary path.

#### Scenario: Fresh install uses Read|Bash

- **WHEN** a user runs `clio install-hook` on a config with no clio hooks
- **THEN** the file-history entry SHALL sit in a PreToolUse group with matcher `Read|Bash`

#### Scenario: Read group upgraded in place

- **WHEN** the settings hold clio's file-history entry alone in a PreToolUse group with
  matcher `Read` (or ` Read `) and the user runs `clio install-hook`
- **THEN** that group's matcher SHALL become `Read|Bash` at the same position with its other
  fields kept, no second group SHALL be added, and the command SHALL report the upgrade

#### Scenario: Matcher covering both tools counts as installed

- **WHEN** clio's entry sits in a group with matcher `Bash|Read`, `Read, Bash`, `*`, or no matcher
- **THEN** `clio install-hook` SHALL add no second group, print no warning and report no upgrade

#### Scenario: Matcher covering only one tool is left alone with a warning

- **WHEN** clio's entry sits in a group with matcher `Read|Edit`, `Bash`, `^Read$`, `Rea.`,
  `Rea` or `Edit`
- **THEN** `clio install-hook` SHALL change nothing, add no group, and warn that the matcher
  does not cover both Read and Bash

#### Scenario: Duplicate entries collapse

- **WHEN** two `Read|Bash` groups each hold a clio file-history entry
- **THEN** after `clio install-hook` exactly one clio entry SHALL remain

#### Scenario: Leftover Read entry beside an existing Read|Bash entry

- **WHEN** a `Read|Bash` group holds clio and a `Read` group shared with a foreign hook also
  holds a clio entry
- **THEN** the leftover entry SHALL be removed, the foreign hook SHALL stay under `Read`, and
  the command SHALL report one collapsed duplicate and no upgrade

#### Scenario: Read group shared with a foreign hook

- **WHEN** a `Read` group holds a foreign hook and clio's entry
- **THEN** after `clio install-hook` the foreign hook SHALL remain alone under `Read` and
  clio's entry SHALL sit alone under a `Read|Bash` group

#### Scenario: Install preserves existing hooks

- **WHEN** a user runs `clio install-hook` with other SessionStart or PreToolUse
  hooks present
- **THEN** clio's two hooks SHALL be added and the existing hooks SHALL remain

#### Scenario: Recall hook skips forked sessions and has a timeout

- **WHEN** `clio install-hook` registers the recall hook
- **THEN** its group SHALL carry matcher `startup|resume|clear|compact` and its entry
  a 10-second timeout

#### Scenario: Re-install after an older clio installed only the recall hook

- **WHEN** the settings already hold clio's SessionStart recall hook in a group
  without a matcher, and no PreToolUse file-history hook
- **THEN** `clio install-hook` SHALL add the PreToolUse hook and upgrade the recall
  entry in place to the matcher and timeout, leaving exactly one recall entry

#### Scenario: Executable path containing a space

- **WHEN** the clio binary lives at `/Users/me/My Tools/clio` and a user runs
  `clio install-hook` twice, then `clio uninstall-hook`
- **THEN** the hook commands SHALL be `'/Users/me/My Tools/clio' recall` and
  `'/Users/me/My Tools/clio' file-history --hook`, the second run SHALL add no
  duplicate, and uninstall SHALL remove both

#### Scenario: Opting out of file context

- **WHEN** a user runs `clio install-hook --no-file-context`
- **THEN** only the SessionStart recall hook SHALL be registered, and a file-history
  hook left by an earlier install SHALL be removed

#### Scenario: Uninstall removes only clio's hooks

- **WHEN** a user runs `clio uninstall-hook`
- **THEN** only clio's SessionStart recall hook (with or without a matcher) and
  PreToolUse file-history hook SHALL be removed, leaving other hooks intact
