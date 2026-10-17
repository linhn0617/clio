## ADDED Requirements

### Requirement: Terminal escape sequences are stripped from TUI text

The TUI SHALL NOT pass terminal control sequences from indexed text through to the
terminal. Row labels (titles, project paths, snippets, activity values, subagent
types), the preview pane's message content, and the Ask view's excerpts SHALL have
ESC-introduced sequences (CSI, OSC, DCS, APC, PM, SOS, and other ESC sequences; an
unterminated one is dropped to the end of the string, unless a new ESC appears
first — that aborts the earlier sequence and parsing restarts there, as a terminal
does), C0 controls other than newline and tab, DEL, and C1 controls (UTF-8 encoded
or raw; a C1 introducer or ST acts like its ESC form) removed before rendering. Ordinary text, CJK, and emoji SHALL be preserved.

#### Scenario: Preview pane drops escape sequences

- **WHEN** a previewed message contains `ESC ] 52 ; c ; <base64> BEL` or
  `ESC [ 2 J`
- **THEN** the rendered preview SHALL contain no `ESC` byte and SHALL keep the
  message's visible text

#### Scenario: List rows drop escape sequences

- **WHEN** a session title or search snippet contains an OSC window-title sequence
- **THEN** the row SHALL render only the visible text on a single line
