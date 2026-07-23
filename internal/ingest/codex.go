package ingest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/linhn0617/clio/internal/model"
)

// codexUUIDRe matches a UUID; the last match in a Codex rollout filename
// (rollout-<ISO-timestamp>-<uuid>.jsonl) is the session id.
var codexUUIDRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// codexSource ingests OpenAI Codex CLI transcripts: append-only JSONL rollout files
// under ~/.codex/sessions. The conversation is taken from the response_item stream
// (the duplicate event_msg UI stream is skipped) and harness wrapper blocks are
// stripped from user turns.
type codexSource struct {
	root string
}

func (codexSource) Name() string { return model.SourceCodex }

// Owns matches rollout-*.jsonl files under this source's root.
func (s codexSource) Owns(path string) bool {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "rollout-") || !strings.HasSuffix(base, ".jsonl") {
		return false
	}
	return s.root != "" && pathUnder(path, s.root)
}

// SessionIDFromPath returns the trailing UUID in a rollout filename.
func (codexSource) SessionIDFromPath(path string) string {
	matches := codexUUIDRe.FindAllString(filepath.Base(path), -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func (s codexSource) Roots() ([]string, error) {
	if s.root == "" {
		return nil, nil
	}
	return []string{s.root}, nil
}

func (codexSource) WholeFileReplay() bool { return false }

// pathUnder reports whether path is within root.
func pathUnder(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// codexEnvelope is one line of a rollout file: {timestamp, type, payload}.
type codexEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// codexPayload holds the union of payload fields clio reads across record types.
type codexPayload struct {
	Type      string          `json:"type"`      // inner type for response_item / event_msg
	Role      string          `json:"role"`      // message
	Content   json.RawMessage `json:"content"`   // message: block array
	Summary   json.RawMessage `json:"summary"`   // reasoning: block array
	Name      string          `json:"name"`      // function_call
	Arguments string          `json:"arguments"` // function_call: JSON string
	CallID    string          `json:"call_id"`   // function_call / function_call_output
	Output    json.RawMessage `json:"output"`    // function_call_output: string or blocks
	ID        string          `json:"id"`        // session_meta
	CWD       string          `json:"cwd"`       // session_meta / turn_context
}

type codexBlock struct {
	Type string `json:"type"` // input_text | output_text | text
	Text string `json:"text"`
}

// ParseFile parses a Codex rollout file from startOffset into a session and messages.
// The response_item stream is authoritative; the event_msg stream is skipped to avoid
// double-counting. The filename uuid is canonical: if session_meta.id disagrees,
// the mismatch is logged and the filename uuid wins so the file still gets indexed.
func (s codexSource) ParseFile(ing *Ingester, f *os.File, startOffset int64, startSeq int, path string) (parseResult, error) {
	if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
		return parseResult{}, err
	}
	sess := model.Session{UUID: s.SessionIDFromPath(path), SourceFile: path, Source: model.SourceCodex}
	var msgs []model.Message
	var consumed, unparsed int64
	seq := startSeq
	skipped := map[string]bool{} // call_ids of skipped clio MCP calls, so their outputs drop too

	// Usage extraction: token_count events carry a CUMULATIVE whole-file counter,
	// so the latest observation IS the file's total — even a tail-only read yields
	// the true value (one rollout file = one session). The counter has no per-model
	// attribution; rows always use the "(mixed)" sentinel.
	var lastTokenUsage map[string]json.RawMessage
	var usageSkipped int64
	snapshots := map[string]model.QuotaSnapshot{}

	add := func(ts int64, role, content, raw string, tcs []model.ToolCall, targets []model.ToolTarget) {
		content = strings.TrimSpace(content)
		if content == "" && len(tcs) == 0 {
			return
		}
		msgs = append(msgs, model.Message{
			SessionUUID: sess.UUID,
			Seq:         seq,
			TS:          ts,
			Role:        role,
			Content:     truncateForFTS(redactString(content)),
			RawJSON:     raw,
			ToolCalls:   tcs,
			Targets:     targets,
		})
		seq++
	}

	r := bufio.NewReader(f)
	for {
		data, n, terminated, overCap, err := readCappedLine(r, maxLineBytes)
		if err != nil && err != io.EOF {
			return parseResult{}, err
		}
		if !terminated {
			break
		}
		consumed += int64(n)
		if overCap {
			ing.log.Warn("skip over-cap line", "file", path, "bytes", n)
			unparsed++
			continue
		}
		line := bytes.TrimSuffix(data, []byte("\n"))
		if len(line) == 0 {
			continue
		}
		var env codexEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			ing.log.Warn("skip malformed codex line", "file", path, "err", err)
			unparsed++
			continue
		}
		ts := parseTS(env.Timestamp)
		if ts != 0 {
			if sess.StartedAt == 0 || ts < sess.StartedAt {
				sess.StartedAt = ts
			}
			if ts > sess.EndedAt {
				sess.EndedAt = ts
			}
		}
		// event_msg duplicates response_item for conversation content and is
		// skipped for indexing — but its token_count payloads are the only
		// carrier of usage and rate-limit state, so extract those first.
		if env.Type == "event_msg" {
			var tc codexTokenCountPayload
			if err := json.Unmarshal(env.Payload, &tc); err != nil {
				// Undecodable payload: only count it against usage when it was
				// identifiably a token_count (substring check — role unknowable
				// otherwise), so an unrelated broken event_msg is not miscounted.
				if bytes.Contains(env.Payload, tokenCountNeedle) {
					usageSkipped++
				}
				continue
			}
			if tc.Type == "token_count" {
				// A native-total source's record must carry a usable native
				// total; accepting categories without one would persist
				// total_tokens=0 as apparently-current data. Reject + count,
				// keep the previous observation.
				if tc.Info != nil && hasNumericKey(tc.Info.TotalTokenUsage, "total_tokens") {
					lastTokenUsage = tc.Info.TotalTokenUsage // cumulative: latest wins
				} else {
					usageSkipped++
				}
				codexCollectSnapshots(snapshots, tc.RateLimits, ts)
			}
			continue
		}
		// Skip any other non-conversational record type.
		if env.Type != "response_item" && env.Type != "session_meta" && env.Type != "turn_context" {
			continue
		}
		var p codexPayload
		if len(env.Payload) > 0 {
			_ = json.Unmarshal(env.Payload, &p)
		}
		switch env.Type {
		case "session_meta":
			if p.ID != "" {
				if sess.UUID != "" && p.ID != sess.UUID {
					// Filename uuid is canonical: keep it and skip this record's id
					// instead of dropping the whole file from the index.
					ing.log.Warn("codex session id mismatch; keeping filename uuid", "file", path, "filename_uuid", sess.UUID, "session_meta_id", p.ID)
				} else {
					sess.UUID = p.ID
				}
			}
			if sess.ProjectPath == "" && p.CWD != "" {
				sess.ProjectPath = p.CWD
			}
		case "turn_context":
			if sess.ProjectPath == "" && p.CWD != "" {
				sess.ProjectPath = p.CWD
			}
		case "response_item":
			raw := string(redactJSON(line))
			switch p.Type {
			case "message":
				switch p.Role {
				case "user":
					if text := codexUserText(p.Content); text != "" {
						add(ts, model.RoleUser, text, raw, nil, nil)
						if sess.Title == "" {
							sess.Title = titleFrom(redactString(text))
						}
					}
				case "assistant":
					add(ts, model.RoleAssistant, codexBlocksText(p.Content), raw, nil, nil)
					// "developer" is harness/system instructions: skipped.
				}
			case "reasoning":
				if text := codexBlocksText(p.Summary); text != "" {
					add(ts, model.RoleThinking, text, raw, nil, nil)
				}
			case "function_call":
				// Skip clio's own MCP traffic entirely (call + its output), mirroring the
				// Claude path, so clio never indexes its own MCP calls/results. No call_id
				// presence guard: the Claude path has none either, and keying on "" still
				// drops a matching empty-id output (only ever crafted/corrupt input).
				if strings.HasPrefix(p.Name, clioMCPToolPrefix) {
					skipped[p.CallID] = true
					continue
				}
				args := json.RawMessage(p.Arguments)
				summary := codexToolSummary(p.Name, args)
				add(ts, model.RoleToolUse, strings.TrimSpace(p.Name+" "+summary), raw,
					[]model.ToolCall{{ToolName: p.Name, ParamsSummary: summary}}, codexExtractTargets(p.Name, args))
			case "function_call_output":
				if skipped[p.CallID] {
					continue
				}
				add(ts, model.RoleToolResult, codexOutputText(p.Output), raw, nil, nil)
			}
		}
	}
	for i := range msgs {
		msgs[i].SessionUUID = sess.UUID // canonical id, in case session_meta followed early records
	}

	// Usage outcome: replace (a counter was observed), no-op (incremental tail
	// without token events — existing row stays), or delete (a full parse of the
	// whole file found no usage, so any stale row must go).
	usage := &usageResult{Skipped: usageSkipped, AccumulateCounters: startOffset > 0}
	if lastTokenUsage != nil {
		acc := newUsageAcc(model.SourceCodex)
		usage.Unmapped = acc.addCategories(model.ModelMixed, lastTokenUsage, codexUsageCanon, "total_tokens")
		usage.Rows = fillSessionUUID(acc.finish("", false), sess.UUID)
		usage.Outcome = usageReplace
		if len(usage.Rows) == 0 {
			usage.Outcome = usageDelete // counter present but all-zero: nothing to keep
		}
	} else if startOffset == 0 {
		usage.Outcome = usageDelete
	} else {
		usage.Outcome = usageNoop
	}
	for _, snap := range snapshots {
		usage.Snapshots = append(usage.Snapshots, snap)
	}
	return parseResult{Session: sess, Messages: msgs, Consumed: consumed, Unparsed: unparsed, Usage: usage}, nil
}

// tokenCountNeedle identifies an undecodable event_msg payload as a
// token_count for diagnostic counting (see the decode-failure path above).
var tokenCountNeedle = []byte(`"token_count"`)

// codexTokenCountPayload decodes an event_msg token_count payload: the
// cumulative usage counter plus the rate-limit state Codex persists alongside.
type codexTokenCountPayload struct {
	Type string `json:"type"`
	Info *struct {
		TotalTokenUsage map[string]json.RawMessage `json:"total_token_usage"`
	} `json:"info"`
	RateLimits json.RawMessage `json:"rate_limits"`
}

// codexRateLimits mirrors the persisted rate_limits object (one window struct
// per primary/secondary).
type codexRateLimits struct {
	LimitID  string `json:"limit_id"`
	PlanType string `json:"plan_type"`
	Primary  *struct {
		UsedPercent   float64 `json:"used_percent"`
		WindowMinutes int64   `json:"window_minutes"`
		ResetsAt      int64   `json:"resets_at"`
	} `json:"primary"`
	Secondary *struct {
		UsedPercent   float64 `json:"used_percent"`
		WindowMinutes int64   `json:"window_minutes"`
		ResetsAt      int64   `json:"resets_at"`
	} `json:"secondary"`
}

// codexCollectSnapshots folds one rate_limits payload into snaps (latest
// observed_at wins within the pass; the DB upsert guards across passes). Each
// window becomes its own snapshot under "<limit_id>:primary|secondary" so two
// windows of one limit never clobber each other.
func codexCollectSnapshots(snaps map[string]model.QuotaSnapshot, raw json.RawMessage, ts int64) {
	if len(raw) == 0 || ts == 0 {
		return
	}
	var rl codexRateLimits
	if json.Unmarshal(raw, &rl) != nil || rl.LimitID == "" {
		return
	}
	rawStored := string(redactJSON(raw))
	put := func(suffix string, usedPercent float64, windowMinutes, resetsAt int64) {
		key := rl.LimitID + ":" + suffix
		if prev, ok := snaps[key]; ok && prev.ObservedAt >= ts {
			return
		}
		snaps[key] = model.QuotaSnapshot{
			Source: model.SourceCodex, LimitID: key, ObservedAt: ts,
			UsedPercent: usedPercent, WindowMinutes: windowMinutes, ResetsAt: resetsAt,
			PlanType: rl.PlanType, RawJSON: rawStored,
		}
	}
	if rl.Primary != nil {
		put("primary", rl.Primary.UsedPercent, rl.Primary.WindowMinutes, rl.Primary.ResetsAt)
	}
	if rl.Secondary != nil {
		put("secondary", rl.Secondary.UsedPercent, rl.Secondary.WindowMinutes, rl.Secondary.ResetsAt)
	}
}

// codexBlocksText joins the text of input_text/output_text/text blocks.
func codexBlocksText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var blocks []codexBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// codexUserText joins a user message's blocks, dropping harness wrapper blocks
// (<environment_context>, <*_instructions>) so the real prompt survives and a
// wrapper-only injection normalizes to empty (not a turn).
func codexUserText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var blocks []codexBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		t := strings.TrimSpace(b.Text)
		if t == "" || isCodexWrapper(t) {
			continue
		}
		parts = append(parts, b.Text)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// isCodexWrapper reports whether a user text block is a harness-injected wrapper
// (<environment_context> or <something_instructions>) rather than the human's prompt.
func isCodexWrapper(t string) bool {
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "<environment_context") {
		return true
	}
	if strings.HasPrefix(t, "<") {
		if i := strings.IndexByte(t, '>'); i > 1 {
			return strings.HasSuffix(t[1:i], "_instructions")
		}
	}
	return false
}

// codexCommandTarget returns the domain activity fact (command or file) for a Codex
// tool call, or ok=false for tool-only calls. The returned value is raw; callers redact it.
//   - exec_command → its `cmd` string.
//   - shell        → the executed script in its `command` argv (see codexShellCommand).
//   - view_image   → its `path`.
func codexCommandTarget(name string, args json.RawMessage) (kind, value string, ok bool) {
	switch name {
	case "exec_command":
		var a struct {
			Cmd string `json:"cmd"`
		}
		if json.Unmarshal(args, &a) == nil {
			if v := strings.TrimSpace(a.Cmd); v != "" {
				return model.TargetCommand, v, true
			}
		}
	case "shell":
		var a struct {
			Command json.RawMessage `json:"command"`
		}
		if json.Unmarshal(args, &a) == nil {
			if v := codexShellCommand(a.Command); v != "" {
				return model.TargetCommand, v, true
			}
		}
	case "view_image":
		var a struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(args, &a) == nil {
			if v := strings.TrimSpace(a.Path); v != "" {
				return model.TargetFile, v, true
			}
		}
	}
	return "", "", false
}

// codexShellCommand extracts the executed script from a Codex `shell` command value,
// which is either a plain string or an argv array (always ["bash","-lc",script] in the
// corpus). For an argv array it returns the element after the shell command flag (-c,
// or a combined short flag ending in c such as -lc); if no such flag is present it joins
// the argv. Empty string if nothing usable.
func codexShellCommand(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var argv []string
	if json.Unmarshal(raw, &argv) != nil || len(argv) == 0 {
		return ""
	}
	// Only parse a script-after-flag when argv is actually launching a shell (always
	// ["bash","-lc",script] in the corpus); otherwise the joined argv is the best command
	// text. This keeps a non-shell flag like "-abc" from being misread as a command flag.
	if isShellInterpreter(argv[0]) {
		for i := 1; i < len(argv); i++ {
			if isShellCommandFlag(argv[i]) {
				// A command flag with no usable following element is a malformed /
				// scriptless invocation: yield no command, not the wrapper argv.
				if i+1 < len(argv) {
					return strings.TrimSpace(argv[i+1])
				}
				return ""
			}
		}
	}
	return strings.TrimSpace(strings.Join(argv, " "))
}

// isShellInterpreter reports whether arg names a shell (optionally path-qualified) —
// i.e. argv[0] of a `bash -lc <script>`-style invocation.
func isShellInterpreter(arg string) bool {
	if i := strings.LastIndexByte(arg, '/'); i >= 0 {
		arg = arg[i+1:]
	}
	switch arg {
	case "bash", "sh", "zsh", "dash", "ksh":
		return true
	}
	return false
}

// isShellCommandFlag reports whether f is a shell "run this command" flag: -c, or a
// combined short flag ending in c (-lc, -ic, …). Long options (--config) are excluded.
func isShellCommandFlag(f string) bool {
	return f == "-c" || (len(f) >= 2 && f[0] == '-' && f[1] != '-' && strings.HasSuffix(f, "c"))
}

// codexExtractTargets turns one Codex function_call into activity facts: a `tool` fact
// (the tool name) plus a domain fact (command/file) when the call provides one. It
// mirrors the shared extractTargets contract — empty names and clio's own MCP tools
// yield nothing. Values are redacted then capped, like the Claude path.
func codexExtractTargets(name string, args json.RawMessage) []model.ToolTarget {
	if name == "" || strings.HasPrefix(name, clioMCPToolPrefix) {
		return nil
	}
	out := []model.ToolTarget{{Kind: model.TargetTool, Value: name}}
	if kind, value, ok := codexCommandTarget(name, args); ok {
		out = append(out, model.ToolTarget{Kind: kind, Value: capValue(redactString(value))})
	}
	return out
}

// codexToolSummary is the short, human-facing summary of a Codex tool call: the command
// or file it acted on. It redacts the FULL value before truncating, so a secret never
// survives as a partial (regex-missed) token. Empty for tool-only calls.
func codexToolSummary(name string, args json.RawMessage) string {
	if _, value, ok := codexCommandTarget(name, args); ok {
		// firstLine caps on bytes; trim any rune split at the cap so the summary
		// (and the message content built from it) stays valid UTF-8.
		return trimToValidUTF8(firstLine(redactString(value), 200))
	}
	return ""
}

// codexOutputText flattens a function_call_output payload (a bare string, or blocks).
func codexOutputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return codexBlocksText(raw)
}
