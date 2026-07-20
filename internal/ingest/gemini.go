package ingest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/linhn0617/clio/internal/model"
)

// geminiSource ingests Gemini CLI transcripts: an append-only op-log JSONL
// file per session under ~/.gemini/tmp/<projectId>/chats/. Unlike claude-code
// and codex (simple event streams, one line -> zero-or-more messages
// appended in order), a Gemini transcript must be REPLAYED to reconstruct
// the final message list: a record can be a full-state overwrite ($set), a
// bare appended message (upserted by id), or a rewind (design.md §0/§3,
// re-confirmed against a real assistant-bearing transcript, task 6.1 ship
// gate). A $set record with a "messages" key overwrites the reconstructed
// list wholesale (last writer wins); a bare (non-$set, non-$rewindTo)
// top-level message record — confirmed real, not provisional, by a live
// sample containing tool calls and an in-place edit — is replayed via
// upsert-by-id: an id already in the reconstructed list is replaced AT ITS
// ORIGINAL POSITION (the real sample's assistant message arrives first with
// only `thoughts`, then again with `toolCalls` added, same id), an unseen id
// is appended at the tail. $rewindTo remains unobserved in any real
// transcript and is still warned, skipped, and counted as unparsed, never
// replayed — but a line that cannot even be parsed as JSON, or an identified
// $set that is unusable (over-cap or unparsable), aborts the whole pass
// rather than silently discarding partial state: neither case can be ruled
// out as a $set carrying the entire conversation state, so skip-and-continue
// would risk silently discarding a full-state overwrite (design.md §3 P1,
// spec "An unusable Gemini state record aborts the pass"). geminiSource
// therefore declares WholeFileReplay()==true (task 2.1/2.2): the
// orchestrator always reparses it from offset 0 and commits a full
// re-ingest.
// debt: full re-ingest cost grows linearly with session length on every
// change (each watcher batch deletes+reinserts the whole session), where the
// append-only sources pay only for the new tail (design.md §4 "Honest cost
// of this decision"). Accepted deliberately: correctness under $set/rewind
// semantics cannot be had cheaper without a two-mode complexity that was
// evaluated and rejected. Revisit with a size threshold or watcher-side
// coalescing only if real-world use shows re-ingest thrash on long active
// Gemini sessions — do not pre-build either without observed thrash.
type geminiSource struct{ root string } // root = config.GeminiTmpDir(), e.g. ~/.gemini/tmp

func (geminiSource) Name() string { return model.SourceGemini }

// Owns matches *.jsonl files with a "chats" ancestor directory under root —
// covering both main session files (<root>/<projectId>/chats/session-*.jsonl)
// and nested subagent-style children
// (<root>/<projectId>/chats/<parentUUID>/<childUUID>.jsonl, design.md §5). A
// .jsonl file with no "chats" ancestor (old ≤0.1.9 sha256-hash project dirs)
// is not owned; logs.json and checkpoint-*.json are not .jsonl so they are
// never owned either way (spec: "Old and non-chats layouts own no files").
// geminiSource is registered ahead of the claude-code fallback (AddSource
// prepends), so this Owns is consulted first for anything under root.
func (s geminiSource) Owns(path string) bool {
	if s.root == "" || !strings.HasSuffix(path, ".jsonl") || !pathUnder(path, s.root) {
		return false
	}
	rel, err := filepath.Rel(s.root, path)
	if err != nil {
		return false
	}
	for _, part := range strings.Split(filepath.Dir(rel), string(filepath.Separator)) {
		if part == "chats" {
			return true
		}
	}
	return false
}

func (s geminiSource) Roots() ([]string, error) {
	if s.root == "" {
		return nil, nil
	}
	return []string{s.root}, nil
}

func (geminiSource) WholeFileReplay() bool { return true }

// SessionIDFromPath returns the canonical uuid from the transcript's first
// (metadata) line's sessionId field: a main-session filename carries only an
// 8-char fragment (session-<ISO>-<id8>.jsonl), so the full uuid cannot come
// from the filename alone (design.md §2). "" on any read/parse error (e.g. a
// deleted file — purge falls back to the DB, ingest.go sessionUUIDForPurge).
func (geminiSource) SessionIDFromPath(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	meta, ok := readGeminiMetadata(f)
	if !ok {
		return ""
	}
	return meta.SessionID
}

// geminiMetadata is a transcript's first line.
type geminiMetadata struct {
	SessionID   string `json:"sessionId"`
	ProjectHash string `json:"projectHash"`
	StartTime   string `json:"startTime"`
	LastUpdated string `json:"lastUpdated"`
	Kind        string `json:"kind"`
}

// readGeminiMetadata reads and parses a transcript's first line from the
// start of f. ok is false on any read/parse/over-cap error.
func readGeminiMetadata(f *os.File) (geminiMetadata, bool) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return geminiMetadata{}, false
	}
	r := bufio.NewReader(f)
	data, _, terminated, overCap, err := readCappedLine(r, maxLineBytes)
	if !terminated || overCap || (err != nil && err != io.EOF) {
		return geminiMetadata{}, false
	}
	var meta geminiMetadata
	line := bytes.TrimSuffix(data, []byte("\n"))
	if err := json.Unmarshal(line, &meta); err != nil {
		return geminiMetadata{}, false
	}
	return meta, true
}

// geminiProjectID returns the chats/-parent directory name (the projectId)
// for a Gemini transcript path — Owns guarantees a "chats" ancestor exists
// for anything routed here, so this only returns "" for a degenerate path.
func geminiProjectID(path string) string {
	dir := filepath.Dir(path)
	for {
		if filepath.Base(dir) == "chats" {
			return filepath.Base(filepath.Dir(dir))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// geminiProjectsFile is ~/.gemini/projects.json: {"projects": {"<abs project
// path>": "<projectId>"}} — a path -> projectId map, inverted by
// geminiProjectPath to look up by projectId (design.md §1).
type geminiProjectsFile struct {
	Projects map[string]string `json:"projects"`
}

// geminiProjectPath resolves a projectId to its absolute project path by
// inverting geminiRoot's sibling projects.json (root is .../.gemini/tmp;
// projects.json is .../.gemini/projects.json). Returns "" when the file is
// absent, unparsable, or has no entry for id — the session is still
// indexed, just unattributed (spec: "Missing mapping leaves project path
// empty"). Does not parse the project path out of <session_context> prose
// (fragile); projects.json is the one structured source (design.md §1).
func geminiProjectPath(geminiRoot, projectID string) string {
	if projectID == "" || geminiRoot == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(geminiRoot), "projects.json"))
	if err != nil {
		return ""
	}
	var pf geminiProjectsFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return ""
	}
	for absPath, id := range pf.Projects {
		if id == projectID {
			return absPath
		}
	}
	return ""
}

// geminiContentBlock is one element of a Gemini message's content array.
type geminiContentBlock struct {
	Text string `json:"text"`
}

// geminiDecodeContent extracts text blocks from a message's raw "content"
// field, which real gemini-cli transcripts encode in TWO different shapes —
// confirmed by gemini-real-sample-with-assistant.jsonl (ship-gate re-review,
// 2026-07-20), not guessed: a content[] array of {text} blocks (used by
// $set-embedded "user" turns, e.g. lines 2/3), AND a bare JSON string (used
// by bare "gemini"-type records, e.g. line 10's real assistant reply and
// lines 5/7's "" placeholder before real text exists — v1's original model
// only knew the array shape, because the baseline v0.51.0 sample used for
// that guess had no assistant turn at all to reveal this). Both shapes are
// tried; an absent field, an explicit empty string, or any other
// unrecognized JSON shape (object, number, bool, …) all return nil — "no
// extractable text" — and are NOT told apart here, because every caller
// treats them identically (an assistant message with no extractable text is
// warned/skipped/counted; a user message with no extractable text is simply
// not a turn — see the final per-role mapping loop).
func geminiDecodeContent(raw json.RawMessage) []geminiContentBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []geminiContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil
		}
		return []geminiContentBlock{{Text: s}}
	}
	return nil // unrecognized shape (object, number, bool, ...)
}

// geminiMessage is one element of a $set's "messages" array, or a bare
// upserted record (v1 fields only: thoughts/toolCalls are intentionally not
// modeled here — they stay in raw_json unextracted). Their real shapes are
// now confirmed by a live assistant-bearing transcript (ship-gate
// re-review, 2026-07-20, see the gemini-oplog-replay-from-real-sample
// change's design.md) — this is no longer provisional — but extraction
// into separate thinking/tool-use messages remains deliberately out of
// scope for that change too: only the replay/reconstruction model was
// re-confirmed against real bytes, not the extraction itself.
// debt: no thinking/tool-use messages and no activity targets are produced
// from a gemini message's thoughts[]/toolCalls[] yet. Build the extraction
// (mirroring codexExtractTargets, codex.go:378, incl. the mcp__clio__*
// self-pollution guard) in a future change against the confirmed real
// shapes once needed.
type geminiMessage struct {
	ID        string               `json:"id"`
	Timestamp string               `json:"timestamp"`
	Type      string               `json:"type"` // "user" | "gemini" | "info" | "error" | "warning"
	Content   []geminiContentBlock `json:"content"`
}

// geminiMessageEnvelope decodes a $set messages[] element's — OR a bare
// top-level message record's — id/timestamp/type fields while leaving
// content as raw bytes, so an unrecognized content shape (e.g. an object or
// a bare string instead of the expected content[] array) can be handled as
// "no extractable text" at the message level instead of failing to decode
// the whole element outright. Decoding straight into geminiMessage would
// make that failure indistinguishable from a genuine $set-structure failure
// and wrongly abort the entire pass (adversarial review finding 2, P1):
// only the $set's own structure (the wrapper object, the $set value, the
// messages array itself) can hide an unrecoverable full-state overwrite; a
// single messages[] element's shape cannot, because every other element in
// the SAME array is still fully usable — and neither can a single bare
// record's shape, because it is not part of any array at all. See the
// replay loop for where this boundary is enforced, both for $set elements
// and for a bare record's upsert-by-id.
type geminiMessageEnvelope struct {
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content"`
}

// A $set record's value is decoded into a map[string]json.RawMessage (see
// the replay loop), not a struct, so the "messages" key's presence can be
// told apart from its value being the JSON literal null. This distinction is
// load-bearing for the v1 replay rule and the finding-1 fix:
//  1. the "messages" key is ABSENT from the map — a metadata-only $set, a
//     no-op for messages.
//  2. the key IS present with value null — this must NOT be treated like
//     case 1. A struct field of type *json.RawMessage was tried first and
//     rejected: json.Unmarshal explicitly nils a *T struct field on
//     encountering JSON null (the "set the pointer to nil" rule) BEFORE ever
//     reaching RawMessage's own UnmarshalJSON, so a present-but-null value
//     and a genuinely absent key both left the pointer nil — indistinguishable,
//     which is exactly the P1 bug (adversarial review finding 1) this fix
//     exists to close. A map value of type json.RawMessage does not hit that
//     pointer-level short-circuit (RawMessage's UnmarshalJSON runs and
//     captures the literal "null" bytes for a present key), so map lookup's
//     ok flag reliably tells the two cases apart. A present-but-null value
//     cannot be ruled out as a corrupted attempt to carry state, so it is
//     treated as unusable and aborts the pass.
//  3. the key is present with an actual JSON array — the normal case,
//     unmarshaled into the replayed messages.

// isJSONNull reports whether raw is (only) the JSON literal null, used to
// tell a present-but-null value apart from an absent key or real content —
// see the $set-body map decode comment above (finding 1) for why that
// distinction matters.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// geminiReplayedMsg pairs a reconstructed message's parsed fields with raw:
// the bytes of the $set record LINE that produced it (not this element's own
// bytes) — shared by every message the same record carries. This mirrors
// parser.go/codex.go, where every message parsed off one .jsonl line shares
// that line's raw_json (design.md: "the per-message raw_json is the record's
// line"), and is what cli/show.go's writeRaw consecutive-line dedup depends
// on to reconstruct the original file under `clio show --format raw` instead
// of printing one line per message.
type geminiReplayedMsg struct {
	raw    json.RawMessage
	parsed geminiMessage
}

// errUnusableStateRecord marks a parse abort caused by an unusable
// state-carrying record: a Gemini $set that is over-cap or unparsable, a
// present-but-null $set/$set.messages value, an empty metadata sessionId, OR
// a line that cannot even be parsed as a JSON object at all (and so cannot
// be ruled out as having been a $set). IngestFile checks for it with
// errors.Is and durably counts the aborted pass as unparsed
// (recordUnusableStatePass) — the normal unparsed_lines accounting rides the
// commit transaction, which never runs on this path.
//
// Boundary (adversarial review finding 2): this sentinel is for RECORD-level
// failures only — the metadata line, the wrapper object, the $set value, or
// the messages array's own structure. A single messages[] ELEMENT's shape
// (e.g. one message's content field being an unexpected type) is deliberately
// NOT routed through this sentinel: the other elements in the same array are
// still usable, so that failure is skipped and counted like an ordinary
// unobserved-shape line, never aborting the whole pass — see the replay
// loop's per-element geminiMessageEnvelope decode.
var errUnusableStateRecord = errors.New("unusable state record")

// unusableStateCounter is implemented by an errUnusableStateRecord-wrapping
// error that additionally carries priorUnparsed: the count of ordinary
// skip+count records ($rewindTo lines, a bare record with no id or an
// unparsable envelope, or message-element shape skips within an otherwise-
// usable $set) the parser had already accumulated earlier in the SAME
// now-aborted pass. Without this,
// recordUnusableStatePass's durable +1 unparsed_lines bump would silently
// drop those earlier counted skips (adversarial review finding 4, P2) — a
// pass that skip-counted N ordinary records before hitting an unusable state
// record must be counted as N+1, not 1. An error that doesn't implement it
// (or isn't a Gemini unusable-state error) is treated as priorUnparsed=0.
type unusableStateCounter interface {
	PriorUnparsed() int64
}

// geminiUnusableSetError is the concrete error errGeminiUnusableSet returns.
// It wraps errUnusableStateRecord (so errors.Is(err, errUnusableStateRecord)
// keeps working exactly as before) and implements unusableStateCounter.
type geminiUnusableSetError struct {
	path, reason  string
	priorUnparsed int64
}

func (e *geminiUnusableSetError) Error() string {
	return fmt.Sprintf("gemini %s: unusable state record: %s: %s", e.path, e.reason, errUnusableStateRecord)
}

func (e *geminiUnusableSetError) Unwrap() error { return errUnusableStateRecord }

func (e *geminiUnusableSetError) PriorUnparsed() int64 { return e.priorUnparsed }

// errGeminiUnusableSet reports a state-carrying record that could not be
// used: an identified $set that is over the line cap, unparsable, or
// present-but-null (at either the $set or $set.messages level); an empty
// metadata sessionId; OR a line that fails to parse as JSON at all (so it
// might have BEEN a $set — see the call site in the replay loop).
// priorUnparsed is the count of ordinary skip+count records the parser had
// already accumulated earlier in this same pass before hitting this failure
// (0 if none) — see unusableStateCounter. ParseFile returning a non-nil
// error makes IngestFile commit nothing: no session/message rows are
// touched, and the stored byte offset does not advance — so doctor's lag
// check (fi.Size() > offset, doctor.go:309) keeps flagging the file rather
// than going silently stale. The failure itself is still counted as
// unparsed via the errUnusableStateRecord sentinel (spec: "the failure SHALL
// be counted as unparsed"; see recordUnusableStatePass in ingest.go). This
// is the P1 abort-and-preserve contract (design.md §3): "an ordinary skipped
// line loses one record; a $set line that is over-cap or unparsable carries
// the entire conversation state, so skip-and-continue would silently discard
// the whole update" — extended to any line/value that can't be inspected
// well enough to rule that out.
func errGeminiUnusableSet(path, reason string, priorUnparsed int64) error {
	return &geminiUnusableSetError{path: path, reason: reason, priorUnparsed: priorUnparsed}
}

// ParseFile replays a Gemini transcript from offset 0 (startOffset/startSeq
// are always 0 here — forced by ingest.go's whole-file-replay wiring, task
// 2.2) into a session and its messages. The metadata line seeds the session
// (and its sessionId must be non-empty — an empty/missing id would commit
// rows under uuid "" and let unrelated broken files collide there, so it
// aborts like any other corrupted state record, adversarial review finding
// 5). After that, every record is replayed by one of two rules, both
// confirmed against a real assistant-bearing transcript (ship-gate
// re-review, 2026-07-20, gemini-oplog-replay-from-real-sample/design.md —
// this superseded the original v1 guess that a bare record is unreplayed):
//
//   - A $set record with a PRESENT, non-null "messages" key overwrites the
//     ENTIRE reconstructed message list (last writer wins); a metadata-only
//     $set (the "messages" key genuinely absent) is a no-op for messages. An
//     empty "messages" array ([]) is a legitimate full clear — $set is
//     inherently a full-replace operation, and json.Unmarshal into a slice
//     never errors on "[]", so this falls out of the existing decode with no
//     extra branch (pinned by TestGeminiSetEmptyMessagesArrayClearsMessages).
//     A $set whose "messages" key (or whose own value) is present but JSON
//     null is NOT the metadata-only case — it is unusable and aborts
//     (finding 1; see the $set-body map decode comment above
//     geminiMessageEnvelope for why "absent" and "present-but-null" must be
//     told apart).
//   - A bare (non-$set, non-$rewindTo) top-level record is upserted BY ID
//     into the reconstructed list: an id already present is replaced AT ITS
//     ORIGINAL POSITION (never moved to the tail), an unseen id is appended.
//     A bare record with no id cannot be placed by upsert and is skipped +
//     counted, never replayed and never fatal — same for one whose top-level
//     shape fails to decode at all (defensive; should not happen once the
//     line has already parsed as a JSON object). $rewindTo remains
//     unobserved in any real transcript to date and is still warned,
//     skipped, and counted as unparsed, never replayed
//     (`debt:` build it, including the inclusive/exclusive rewind boundary,
//     once a real transcript containing one exists).
//
// A line that does NOT even parse as a JSON object, or an identified $set
// whose own structure is unusable (over-cap, unparsable, or
// present-but-null), aborts the whole pass instead of being skipped: neither
// can be inspected well enough to rule out that it was a $set carrying the
// entire conversation state (see errGeminiUnusableSet; spec "An unusable
// Gemini state record aborts the pass"). A single messages[] ELEMENT's shape
// (as opposed to the $set's own structure), and a single bare record's
// shape, does NOT abort — see the replay loop's per-element/per-record
// geminiMessageEnvelope decode (finding 2, extended to bare records).
func (s geminiSource) ParseFile(ing *Ingester, f *os.File, startOffset int64, startSeq int, path string) (parseResult, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return parseResult{}, err
	}
	r := bufio.NewReader(f)
	var consumed int64
	var unparsed int64

	data, n, terminated, overCap, err := readCappedLine(r, maxLineBytes)
	if err != nil && err != io.EOF {
		return parseResult{}, err
	}
	if !terminated {
		return parseResult{}, nil // no complete metadata line yet: nothing to ingest this pass
	}
	consumed += int64(n)
	if overCap {
		return parseResult{}, errGeminiUnusableSet(path, "metadata line exceeds line cap", unparsed)
	}
	var meta geminiMetadata
	metaLine := bytes.TrimSuffix(data, []byte("\n"))
	if err := json.Unmarshal(metaLine, &meta); err != nil {
		return parseResult{}, fmt.Errorf("gemini %s: unparsable metadata line: %w", path, err)
	}
	if meta.SessionID == "" {
		// Fail-closed, same semantics as a corrupted metadata/$set record
		// (finding 5, P2): an empty uuid must never reach commit().
		return parseResult{}, errGeminiUnusableSet(path, "metadata sessionId is empty", unparsed)
	}

	sess := model.Session{UUID: meta.SessionID, SourceFile: path, Source: model.SourceGemini}
	sess.ProjectPath = geminiProjectPath(s.root, geminiProjectID(path))
	// sess.ParentSession/AgentType are intentionally left empty: nested
	// Gemini children are indexed flat in v1, not linked to their parent
	// (design.md §5, spec "Nested Gemini transcripts are indexed as flat
	// sessions").
	// debt: nested Gemini children are indexed but not linked to their
	// parent (AgentType stays empty too); build the linking
	// (parent-dir-name -> ParentSession, agent-type if the child metadata
	// carries one) in task 6.1 once a real nested transcript is observed.

	var reconstructed []geminiReplayedMsg
	// idIndex maps a message id to its position in reconstructed, so a bare
	// record's upsert-by-id can find and replace an existing entry AT ITS
	// ORIGINAL POSITION in O(1) instead of a linear scan, and so a fresh id
	// can be told apart from one already seen. Rebuilt wholesale every time a
	// $set overwrites reconstructed (see below); updated incrementally by
	// each bare-record upsert in between $sets.
	idIndex := make(map[string]int)

	for {
		data, n, terminated, overCap, err := readCappedLine(r, maxLineBytes)
		if err != nil && err != io.EOF {
			return parseResult{}, err
		}
		if !terminated {
			break // partial trailing line: leave for next pass
		}
		consumed += int64(n)
		if overCap {
			// Cannot inspect an over-cap line's content. $set is the only
			// record shape that legitimately carries growing cumulative
			// state, so any over-cap line is treated conservatively as an
			// unusable $set rather than risking silent loss of a full-state
			// overwrite (design.md §3: "abort-and-preserve is the
			// loss-free choice").
			// debt: if a real session ever hits the 16 MiB line cap
			// (maxLineBytes, incremental.go), revisit — raise the cap or
			// stream-parse $set records; until observed, abort-and-flag is
			// the loss-free choice.
			return parseResult{}, errGeminiUnusableSet(path, fmt.Sprintf("line exceeds %d byte cap", maxLineBytes), unparsed)
		}
		line := bytes.TrimSuffix(data, []byte("\n"))
		if len(line) == 0 {
			continue
		}

		var wrapper map[string]json.RawMessage
		if err := json.Unmarshal(line, &wrapper); err != nil {
			// Not even a JSON object: the record shape can't be inspected, so
			// it can't be ruled out as a $set carrying the entire
			// conversation state. Treat it the same as an
			// identified-but-unparsable $set — abort the whole pass rather
			// than risk silently discarding a full-state overwrite
			// (design.md §3 P1, spec "An unusable Gemini state record aborts
			// the pass"). This is deliberately content-blind: it aborts
			// whether or not the broken bytes happen to mention "$set".
			// Skip-and-continue is reserved for lines that DID parse as a
			// JSON object but carry an unobserved (non-$set) shape, below.
			return parseResult{}, errGeminiUnusableSet(path, "unparsable record line: "+err.Error(), unparsed)
		}
		setRaw, isSet := wrapper["$set"]
		if !isSet {
			if _, isRewind := wrapper["$rewindTo"]; isRewind {
				// $rewindTo remains unobserved in any real transcript to date
				// (ship-gate re-review, 2026-07-20) — still not replayed.
				// debt: build the rewind replay branch (incl. the
				// inclusive/exclusive boundary) once a real transcript
				// containing one exists.
				ing.log.Warn("skip unobserved gemini record shape ($rewindTo, not yet replayed)", "file", path)
				unparsed++
				continue
			}
			// A bare (non-$set, non-$rewindTo) top-level record is a
			// MessageRecord appended directly to the op-log — CONFIRMED real
			// by a live assistant-bearing transcript (ship-gate re-review,
			// 2026-07-20: gemini-real-sample-with-assistant.jsonl lines
			// 3/5/7/8/10), not the "unobserved, skip+count" guess v1 made
			// before that sample existed. It is replayed via upsert-by-id
			// into reconstructed, exactly like a $set's array elements are
			// replayed into the array — never simply skipped.
			var env geminiMessageEnvelope
			if err := json.Unmarshal(line, &env); err != nil {
				// Structurally shouldn't happen (line already parsed as a
				// JSON object to build wrapper above), but guard
				// defensively: this is an ordinary record-level issue, not a
				// $set, so it is skipped and counted, never aborts the pass.
				ing.log.Warn("skip unparsable bare gemini record", "file", path, "err", err)
				unparsed++
				continue
			}
			if env.ID == "" {
				// No id: upsert-by-id has nothing to key on, so this record
				// cannot be placed in the reconstructed list at all — skip
				// and count, same as any other record-level issue that
				// isn't a $set (it cannot hide a full-state overwrite).
				ing.log.Warn("skip bare gemini record with no id", "file", path)
				unparsed++
				continue
			}
			// geminiDecodeContent mirrors the $set-element handling below
			// exactly (array-shape OR bare-string-shape, else nil) and lets
			// the per-role handling in the final mapping loop decide what an
			// empty result means. This is NOT an immediate skip for a
			// shape-mismatch: the record still occupies its upsert-by-id
			// slot, same as a $set element would.
			gm := geminiMessage{ID: env.ID, Timestamp: env.Timestamp, Type: env.Type, Content: geminiDecodeContent(env.Content)}
			rm := geminiReplayedMsg{raw: line, parsed: gm}
			if idx, ok := idIndex[env.ID]; ok {
				reconstructed[idx] = rm // upsert: same id updates in place, position preserved
			} else {
				idIndex[env.ID] = len(reconstructed)
				reconstructed = append(reconstructed, rm)
			}
			continue
		}
		if isJSONNull(setRaw) {
			// The "$set" key is present but its value is the JSON literal
			// null — not the metadata-only shape (which omits the whole
			// "messages" key, not the whole $set value) and not a usable
			// state carrier either. Cannot be ruled out as a corrupted
			// full-state overwrite, so it aborts like any other unusable
			// $set (finding 1, P1).
			return parseResult{}, errGeminiUnusableSet(path, "$set value is null", unparsed)
		}
		// Decode the $set body into a map, not a struct: a struct field of
		// type *json.RawMessage was tried first and rejected, because
		// json.Unmarshal nils a *T struct field on JSON null before ever
		// reaching RawMessage's own UnmarshalJSON — a present-but-null
		// "messages" value and a genuinely absent "messages" key both left
		// the pointer nil, indistinguishable (see the map's doc comment
		// above). A map's ok flag from a plain lookup does not have that
		// problem.
		var body map[string]json.RawMessage
		if err := json.Unmarshal(setRaw, &body); err != nil {
			return parseResult{}, errGeminiUnusableSet(path, "unparsable $set body: "+err.Error(), unparsed)
		}
		messagesRaw, hasMessages := body["messages"]
		if !hasMessages {
			continue // metadata-only $set: the "messages" key is genuinely absent
		}
		if isJSONNull(messagesRaw) {
			// The "messages" key IS present but its value is null — distinct
			// from the metadata-only case above (key absent). Silently
			// treating this like "leave messages unchanged" is exactly the
			// P1 bug: a present-but-null messages value cannot be ruled out
			// as a corrupted attempt to overwrite state, so it aborts
			// (finding 1, P1).
			return parseResult{}, errGeminiUnusableSet(path, "$set messages value is null", unparsed)
		}
		var rawMsgs []json.RawMessage
		if err := json.Unmarshal(messagesRaw, &rawMsgs); err != nil {
			return parseResult{}, errGeminiUnusableSet(path, "unparsable $set messages array: "+err.Error(), unparsed)
		}
		// An empty array ("messages":[]) unmarshals into a zero-length,
		// non-nil rawMsgs with no error — json.Unmarshal never errors on "[]"
		// for a slice target. Deliberately treated as a legitimate full
		// clear, not an abort: $set is inherently a full-replace operation
		// (§ design.md, "the last $set wins"), so an explicitly empty array
		// is just that replacement being "the empty conversation" — no
		// special-case branch is needed, this falls out of the existing
		// decode (pinned by TestGeminiSetEmptyMessagesArrayClearsMessages;
		// unobserved in any real transcript to date, so this is a considered
		// choice, not an observed fact).
		replayed := make([]geminiReplayedMsg, 0, len(rawMsgs))
		for _, raw := range rawMsgs {
			// Message-level shape problems — this element isn't even a JSON
			// object, or its "content" field is present but not the expected
			// array — are deliberately NOT record-level failures: the other
			// elements in the SAME messages[] array are still fully usable,
			// so a bad element is skipped and counted, never aborting the
			// whole pass (finding 2, P1; see geminiMessageEnvelope's doc
			// comment for the record-vs-element boundary).
			if isJSONNull(raw) {
				// A messages[] element that is the JSON literal null — not an
				// object — is a message-level shape problem like any other
				// (finding 2, P2/adversarial round 3): json.Unmarshal treats
				// decoding "null" into a struct as a no-op (leaves it at its
				// zero value and returns a nil error), so without this check
				// the element below would silently produce a typeless,
				// empty-fielded envelope that the type switch further down
				// drops via its "default" branch with no warning and no
				// unparsed count — a silent record loss. Reject it here with
				// the SAME skip-and-count semantics as a non-object element:
				// the other elements in the same array are still usable, so
				// this does not abort the record.
				ing.log.Warn("skip null gemini message element", "file", path)
				unparsed++
				continue
			}
			var env geminiMessageEnvelope
			if err := json.Unmarshal(raw, &env); err != nil {
				ing.log.Warn("skip unparsable gemini message element", "file", path, "err", err)
				unparsed++
				continue
			}
			// geminiDecodeContent handles BOTH real content shapes (a
			// content[] array of {text} blocks, or a bare JSON string — see
			// its doc comment) and returns nil — "no extractable text" — for
			// anything else (e.g. an object). An empty result is not an
			// element-level failure: the per-role handling below treats it
			// exactly like an empty content[] array — an assistant message
			// with no extractable text is warned, skipped, and counted
			// (spec: "An assistant message with an unrecognized content
			// shape is not indexed empty"); a user message with no
			// extractable text is simply not a turn (same as a
			// session_context-only wrapper).
			gm := geminiMessage{ID: env.ID, Timestamp: env.Timestamp, Type: env.Type, Content: geminiDecodeContent(env.Content)}
			// raw_json is the record's own line (redacted below), shared by
			// every message this $set produces — not this element's bytes.
			// See geminiReplayedMsg's doc comment (finding 3).
			replayed = append(replayed, geminiReplayedMsg{raw: line, parsed: gm})
		}
		reconstructed = replayed // full overwrite, last writer wins
		// idIndex must be rebuilt wholesale (not merged) alongside
		// reconstructed: a $set is a full-state overwrite, so any id that
		// existed only in a PRIOR $set/bare-record generation and is absent
		// from this array is gone — a later bare record referencing that
		// stale id must be treated as a fresh append, not an upsert into a
		// position that no longer exists in the current conversation.
		idIndex = make(map[string]int, len(reconstructed))
		for i, rm := range reconstructed {
			if rm.parsed.ID != "" {
				idIndex[rm.parsed.ID] = i
			}
		}
	}

	var msgs []model.Message
	seq := 0
	// redactCache maps a raw record LINE's own identity (the address of its
	// first byte) to its already-redacted string, so every message that
	// line produced shares ONE redact call and ONE resulting string for the
	// WHOLE mapping pass — not just when consecutive (finding 1,
	// P1/adversarial round 3, re-broken by upsert-by-id and re-found on
	// ship-gate re-review 2026-07-20). An earlier version of this cache only
	// compared each message's raw line against the IMMEDIATELY PRECEDING
	// message's, which was sufficient when a $set's messages were the only
	// thing ever in reconstructed (they're appended together, so they start
	// out adjacent — see the append in the $set branch above and
	// geminiReplayedMsg's doc comment). But upsert-by-id can splice a bare
	// record's line in BETWEEN two messages that came from the SAME $set
	// line without removing either — e.g. $set[A,B,C] then a bare upsert of
	// B produces the reconstructed identity sequence
	// [setLine, bareLine, setLine]: A and C still share setLine's bytes but
	// are no longer adjacent, so a consecutive-only check misses the second
	// occurrence and redacts setLine again — the same line redacted twice,
	// and (since encoding/json never interns strings) the two results no
	// longer share a backing array either, breaking cli/show.go's writeRaw
	// consecutive-line dedup for messages that DO stay consecutive.
	// Keying by the line's identity across the ENTIRE pass, not just the
	// previous iteration, fixes both: wherever a line's messages land after
	// upserts reorder or interleave them, they all hit the same cache entry.
	//
	// The key is the line's own identity, not its content: two structurally
	// different records that happen to redact to identical bytes must still
	// redact independently (they're different records that coincide, not
	// the same record). &raw[0] is a stable, comparable identity for this —
	// every message produced by one record's line shares that exact
	// backing array (geminiReplayedMsg's doc comment), and Go's GC never
	// relocates a live array out from under a still-held reference, which is
	// the same idiom TestGeminiSetRedactedOncePerRecordNotPerMessage already
	// relies on via unsafe.StringData. An empty raw (len 0 — not expected in
	// practice, since both the $set element loop and the bare record loop
	// above only ever store a non-empty line, but guarded defensively since
	// raw[0] cannot be taken on it) is redacted directly and never cached;
	// redacting "" is O(1) either way so that costs nothing.
	redactCache := make(map[*byte]string, len(reconstructed))
	for _, rm := range reconstructed {
		gm := rm.parsed
		ts := parseTS(gm.Timestamp)
		if ts != 0 {
			if sess.StartedAt == 0 || ts < sess.StartedAt {
				sess.StartedAt = ts
			}
			if ts > sess.EndedAt {
				sess.EndedAt = ts
			}
		}
		var raw string
		if len(rm.raw) == 0 {
			raw = string(redactJSON(rm.raw))
		} else {
			key := &rm.raw[0]
			if cached, ok := redactCache[key]; ok {
				raw = cached
			} else {
				raw = string(redactJSON(rm.raw))
				redactCache[key] = raw
			}
		}
		switch gm.Type {
		case model.RoleUser:
			text := stripGeminiSessionContext(joinGeminiContentText(gm.Content))
			if text == "" {
				continue // empty or wrapper-only: not a turn, not a title source
			}
			redacted := redactString(text)
			msgs = append(msgs, model.Message{SessionUUID: sess.UUID, Seq: seq, TS: ts, Role: model.RoleUser, Content: truncateForFTS(redacted), RawJSON: raw})
			seq++
			if sess.Title == "" {
				sess.Title = titleFrom(redacted)
			}
		case "gemini":
			text := joinGeminiContentText(gm.Content)
			if strings.TrimSpace(text) == "" {
				// Unrecognized content shape: never silently index an empty
				// assistant message (spec).
				ing.log.Warn("gemini assistant message has no extractable text; skipping", "file", path, "id", gm.ID)
				unparsed++
				continue
			}
			msgs = append(msgs, model.Message{SessionUUID: sess.UUID, Seq: seq, TS: ts, Role: model.RoleAssistant, Content: truncateForFTS(redactString(text)), RawJSON: raw})
			seq++
		case "info", "error", "warning":
			// Non-conversational: skipped, not counted as unparsed (a known,
			// intentionally-ignored shape, not a parse failure).
		default:
			// Unrecognized type: not one of the observed shapes. Ignored
			// quietly like info/error/warning rather than counted as
			// unparsed, since this is a message-level field value (not an
			// op-log record shape) and v1 has no evidence of what it means.
		}
	}

	return parseResult{Session: sess, Messages: msgs, Consumed: consumed, Unparsed: unparsed}, nil
}

// joinGeminiContentText joins a Gemini message's content[].text blocks
// (mirrors codexBlocksText).
func joinGeminiContentText(blocks []geminiContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// stripGeminiSessionContext removes <session_context>...</session_context>
// harness-wrapper blocks from s (mirrors codex's <environment_context>
// handling, codex.go isCodexWrapper/codexUserText). Returns "" if s is empty
// or entirely wrapper, so a wrapper-only message is neither a turn nor a
// title source.
func stripGeminiSessionContext(s string) string {
	s = strings.TrimSpace(s)
	const openTag, closeTag = "<session_context>", "</session_context>"
	for {
		start := strings.Index(s, openTag)
		if start < 0 {
			break
		}
		rest := s[start+len(openTag):]
		end := strings.Index(rest, closeTag)
		if end < 0 {
			s = strings.TrimSpace(s[:start]) // unterminated wrapper: drop from the tag onward
			break
		}
		s = strings.TrimSpace(s[:start] + rest[end+len(closeTag):])
	}
	return s
}
