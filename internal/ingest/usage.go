package ingest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/linhn0617/clio/internal/model"
)

// usageOutcome is the aggregate write a usage pass produced. Every source's
// aggregate is a pure function of complete state (design D2), so the only
// operations are: replace this session's rows, delete them (complete state
// yields no usage), no-op (a Codex tail with no token events), or scan-failed
// (atomic no-op + the file's usage_stale flag; old rows retained).
type usageOutcome int

const (
	usageNoop usageOutcome = iota
	usageReplace
	usageDelete
	usageScanFailed
)

// usageResult carries one parse pass's usage extraction: the session's
// replacement aggregate rows, any quota snapshots observed, and the per-file
// diagnostic counters. AccumulateCounters is true only for tail-scoped
// extraction (Codex incremental) — whole-file passes replace the counters.
type usageResult struct {
	Outcome            usageOutcome
	Rows               []model.SessionUsage
	Snapshots          []model.QuotaSnapshot
	Skipped            int64
	Unmapped           int64
	AccumulateCounters bool
}

// usageAcc accumulates per-model canonical category sums plus source-specific
// extras during one usage pass.
type usageAcc struct {
	source string
	rows   map[string]*model.SessionUsage
	cats   map[string]map[string]int64
	order  []string // model insertion order, for deterministic output
}

func newUsageAcc(source string) *usageAcc {
	return &usageAcc{source: source, rows: map[string]*model.SessionUsage{}, cats: map[string]map[string]int64{}}
}

func (a *usageAcc) row(mdl string) *model.SessionUsage {
	if mdl == "" {
		mdl = model.ModelUnknown
	}
	r, ok := a.rows[mdl]
	if !ok {
		r = &model.SessionUsage{Source: a.source, Model: mdl}
		a.rows[mdl] = r
		a.cats[mdl] = map[string]int64{}
		a.order = append(a.order, mdl)
	}
	return r
}

// addCategories folds a decoded usage/tokens object into the accumulator for
// mdl. canon maps source key -> canonical column setter; a numeric key not in
// canon lands in categories (returned unmappedDelta counts those occurrences).
// nativeTotalKey names the source's native total field ("" = none; Claude).
// Non-numeric values are ignored (structured request counters, tier strings —
// not token categories).
func (a *usageAcc) addCategories(mdl string, usage map[string]json.RawMessage, canon map[string]func(*model.SessionUsage, int64), nativeTotalKey string) (unmappedDelta int64) {
	r := a.row(mdl)
	if mdl == "" {
		mdl = model.ModelUnknown
	}
	for k, raw := range usage {
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			continue // non-numeric: not a token category
		}
		if k == nativeTotalKey && nativeTotalKey != "" {
			r.TotalTokens += n
			continue
		}
		if set, ok := canon[k]; ok {
			set(r, n)
			continue
		}
		a.cats[mdl][k] += n
		unmappedDelta++
	}
	return unmappedDelta
}

// finish materializes the accumulated rows for sessionUUID. deriveTotal, when
// true, computes total_tokens as the fixed derived sum input+output+
// cache_read+cache_creation (Claude — no native total exists). Extras are
// serialized as canonical (sorted-key) JSON.
func (a *usageAcc) finish(sessionUUID string, deriveTotal bool) []model.SessionUsage {
	out := make([]model.SessionUsage, 0, len(a.rows))
	for _, mdl := range a.order {
		r, ok := a.rows[mdl]
		if !ok {
			continue
		}
		r.SessionUUID = sessionUUID
		if deriveTotal {
			r.TotalTokens = r.InputTokens + r.OutputTokens + r.CacheRead + r.CacheCreation
		}
		if cats := a.cats[mdl]; len(cats) > 0 {
			r.CategoriesJSON = canonicalCategoriesJSON(cats)
		}
		if r.TotalTokens == 0 && r.InputTokens == 0 && r.OutputTokens == 0 && r.CacheRead == 0 &&
			r.CacheCreation == 0 && r.Reasoning == 0 && r.Tool == 0 && r.CategoriesJSON == "" {
			continue // all-zero row carries no information
		}
		out = append(out, *r)
	}
	return out
}

// canonicalCategoriesJSON serializes category sums with sorted keys so the
// stored JSON is deterministic.
func canonicalCategoriesJSON(cats map[string]int64) string {
	keys := make([]string, 0, len(cats))
	for k := range cats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		sb.Write(kb)
		sb.WriteByte(':')
		vb, _ := json.Marshal(cats[k])
		sb.Write(vb)
	}
	sb.WriteByte('}')
	return sb.String()
}

// claudeUsageCanon maps Claude message.usage keys to canonical columns.
var claudeUsageCanon = map[string]func(*model.SessionUsage, int64){
	"input_tokens":                func(r *model.SessionUsage, n int64) { r.InputTokens += n },
	"output_tokens":               func(r *model.SessionUsage, n int64) { r.OutputTokens += n },
	"cache_read_input_tokens":     func(r *model.SessionUsage, n int64) { r.CacheRead += n },
	"cache_creation_input_tokens": func(r *model.SessionUsage, n int64) { r.CacheCreation += n },
}

// Pre-filter needles for the Claude usage scan (see scanClaudeUsage).
var (
	usageKeyNeedle      = []byte(`"usage"`)
	assistantTypeNeedle = []byte(`"type":"assistant"`)
)

// claudeUsageEvent decodes only the fields the usage scan needs from one line.
type claudeUsageEvent struct {
	Type    string `json:"type"`
	UUID    string `json:"uuid"`
	Message *struct {
		Model string                     `json:"model"`
		Usage map[string]json.RawMessage `json:"usage"`
	} `json:"message"`
}

// scanClaudeUsage is the dedicated Claude usage pass: it re-reads the ORIGINAL
// session file over exactly [0, limit) — the committed complete-line watermark
// — extracting only (outer uuid, message.model, message.usage). The filtered
// messages table must never be the usage source: the message parser drops
// clio-MCP tool-use events entirely, yet those events still carry usage, and
// stored raw_json is redaction-processed. Dedupe is by outer uuid, later line
// in the file wins. Runs outside the write transaction (design D2).
//
// Failure semantics: an oversized line is skipped and counted (matching text
// ingest's skip-and-continue), never fatal; a hard read error mid-scan returns
// Outcome usageScanFailed — an atomic no-op whose retained old rows get the
// file's usage_stale flag. A completed scan with no usage yields usageDelete.
func scanClaudeUsage(f *os.File, limit int64, path string, log *slog.Logger) *usageResult {
	res := &usageResult{}
	if limit <= 0 {
		res.Outcome = usageDelete
		return res
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		res.Outcome = usageScanFailed
		log.Warn("usage scan seek failed", "file", path, "err", err)
		return res
	}
	type lastUsage struct {
		model string
		usage map[string]json.RawMessage // nil = last state for this uuid carried no usage
	}
	// perUUID: literal last-wins by outer uuid — the LATER line's state replaces
	// the earlier one entirely, including "no usage" (a usage-less later
	// occurrence tombstones previously seen usage; spec: later line in file
	// wins). uuidOrder keeps first-seen order for deterministic output. A
	// usage-bearing line without an outer uuid cannot participate in
	// deduplication at all and is counted as malformed (usage_skipped), never
	// summed — summing it could double-count.
	perUUID := map[string]lastUsage{}
	var uuidOrder []string

	r := bufio.NewReaderSize(io.LimitReader(f, limit), 512<<10)
	for {
		data, _, terminated, overCap, err := readCappedLine(r, maxLineBytes)
		if err != nil && err != io.EOF {
			res.Outcome = usageScanFailed
			log.Warn("usage scan read failed", "file", path, "err", err)
			return res
		}
		if !terminated {
			break // end of watermark region
		}
		if overCap {
			res.Skipped++ // role unknowable without parsing: conservative over-count, accepted
			continue
		}
		line := data
		if len(line) == 0 {
			continue
		}
		// Cheap pre-filters before the JSON decode: a line without a "usage"
		// key cannot contribute, and only assistant events are should-carry.
		// The substring checks may rarely false-positive on content that
		// mentions these keys — the full decode below stays authoritative;
		// they only decide what we bother decoding.
		if !bytes.Contains(line, usageKeyNeedle) {
			// An assistant event WITHOUT any usage key is a should-carry miss —
			// counted, and (literal last-wins) it TOMBSTONES any earlier usage
			// seen for the same uuid: its last state is "no usage".
			if bytes.Contains(line, assistantTypeNeedle) {
				var ev claudeUsageEvent
				if err := json.Unmarshal(line, &ev); err == nil && ev.Type == "assistant" && ev.Message != nil {
					res.Skipped++
					if ev.UUID != "" {
						if _, seen := perUUID[ev.UUID]; !seen {
							uuidOrder = append(uuidOrder, ev.UUID)
						}
						perUUID[ev.UUID] = lastUsage{}
					}
				}
			}
			continue
		}
		var ev claudeUsageEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // text pass already counts malformed lines; role unknowable
		}
		if ev.Type != "assistant" {
			continue // only assistant events carry usage; others are not "should-carry"
		}
		if ev.Message == nil || len(ev.Message.Usage) == 0 {
			res.Skipped++ // an assistant event should carry usage
			if ev.UUID != "" {
				// Tombstone: the later line for this uuid carries no usage, so
				// its last state is "none" — literal last-wins must not keep
				// stale earlier usage for it.
				if _, seen := perUUID[ev.UUID]; !seen {
					uuidOrder = append(uuidOrder, ev.UUID)
				}
				perUUID[ev.UUID] = lastUsage{}
			}
			continue
		}
		if ev.UUID == "" {
			res.Skipped++ // cannot dedupe without a uuid: malformed, never summed
			continue
		}
		if _, seen := perUUID[ev.UUID]; !seen {
			uuidOrder = append(uuidOrder, ev.UUID)
		}
		perUUID[ev.UUID] = lastUsage{model: ev.Message.Model, usage: ev.Message.Usage} // later line wins
	}

	acc := newUsageAcc(model.SourceClaudeCode)
	for _, id := range uuidOrder {
		lu := perUUID[id]
		if lu.usage == nil {
			continue // tombstoned: last state for this uuid carries no usage
		}
		res.Unmapped += acc.addCategories(lu.model, lu.usage, claudeUsageCanon, "")
	}
	res.Rows = acc.finish("", true) // session uuid filled by the caller
	if len(res.Rows) == 0 {
		res.Outcome = usageDelete
	} else {
		res.Outcome = usageReplace
	}
	return res
}

// codexUsageCanon maps Codex total_token_usage keys to canonical columns;
// total_tokens is the native total.
var codexUsageCanon = map[string]func(*model.SessionUsage, int64){
	"input_tokens":            func(r *model.SessionUsage, n int64) { r.InputTokens += n },
	"cached_input_tokens":     func(r *model.SessionUsage, n int64) { r.CacheRead += n },
	"output_tokens":           func(r *model.SessionUsage, n int64) { r.OutputTokens += n },
	"reasoning_output_tokens": func(r *model.SessionUsage, n int64) { r.Reasoning += n },
}

// geminiUsageCanon maps Gemini per-message tokens keys to canonical columns;
// "total" is the native total.
var geminiUsageCanon = map[string]func(*model.SessionUsage, int64){
	"input":    func(r *model.SessionUsage, n int64) { r.InputTokens += n },
	"output":   func(r *model.SessionUsage, n int64) { r.OutputTokens += n },
	"cached":   func(r *model.SessionUsage, n int64) { r.CacheRead += n },
	"thoughts": func(r *model.SessionUsage, n int64) { r.Reasoning += n },
	"tool":     func(r *model.SessionUsage, n int64) { r.Tool += n },
}

// hasNumericKey reports whether m carries key with a plain numeric value —
// used to validate that a native-total source's record actually carries its
// native total before its categories are accepted (a record with categories
// but no usable native total would otherwise persist total_tokens=0 and rank
// as apparently-current wrong data; such records are rejected + counted).
func hasNumericKey(m map[string]json.RawMessage, key string) bool {
	raw, ok := m[key]
	if !ok {
		return false
	}
	var n int64
	return json.Unmarshal(raw, &n) == nil
}

// fillSessionUUID stamps uuid onto rows produced before the session id was known.
func fillSessionUUID(rows []model.SessionUsage, uuid string) []model.SessionUsage {
	for i := range rows {
		rows[i].SessionUUID = uuid
	}
	return rows
}
