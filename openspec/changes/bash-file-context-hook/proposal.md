## Why

The file-history PreToolUse hook only fires for the `Read` tool. In real use most file
reading goes through Bash (`cat`, `sed -n`, `grep` on a path): over a recent 7-day window the
maintainer made 1,665 Bash calls against 132 Read calls, so the hook missed almost every read.
Firing on every Bash call would add a process start and a DB open to commands that read no
file, so the Bash branch must stay free unless the command actually names a file.

## What Changes

- **file-history**: the hook also handles `tool_name == "Bash"`. It tokenises
  `tool_input.command`, keeps tokens that resolve (against the payload `cwd`) to an existing
  regular file strictly under `cwd`, de-duplicated, first three in order of appearance, and
  looks each up exactly like a `Read`. Zero candidates exit before the index is opened. Several
  files yield one segment per file under a single pointer line, within the same 8,000-character
  cap. The `Read` branch is unchanged.
- **recall-hook (installer)**: the PreToolUse group matcher becomes `Read|Bash`. A clio entry
  an older install left in a `Read` group is upgraded: a clio-only group is retargeted in
  place, a group shared with foreign hooks keeps them under `Read` and clio's entry moves to its
  own `Read|Bash` group. `install-hook` reports the upgrade; presence detection accepts either
  matcher; uninstall already ignores the matcher.
- **cli-surface**: no change (the command descriptions there do not name a matcher).
- README and README_zh-TW describe the Bash branch.

## Impact

- Existing installs get the Bash branch on the next `clio install-hook`; until then the old
  `Read` matcher keeps working unchanged.
- Cost: a Bash call naming no existing file under `cwd` pays only process start; one that does
  pays at most three index lookups (two queries each).
- Not handled (`debt:` in code): `$VAR`/`~`/glob expansion and backslash escapes in the command.
