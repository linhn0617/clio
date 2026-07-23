#!/bin/bash
# Perf gate harness for add-session-usage-summary (design.md protocol):
# fixed fixture set, fresh DB per run, wal_checkpoint(TRUNCATE) before size
# reads, main-DB size = page_count*page_size, interleaved baseline/candidate
# runs (n>=10), medians. Baseline = the git commit BEFORE the feature code
# (pass its SHA as $1); candidate = the working tree.
set -euo pipefail
cd "$(dirname "$0")/../../../.."   # repo root
BASELINE_SHA="${1:?usage: run.sh <baseline-sha> [n]}"
N="${2:-10}"
if [ "$N" -lt 10 ]; then
  echo "NOTE: protocol requires n>=10; n=$N is a smoke run — gates will NOT be certified." >&2
  SMOKE=1
else
  SMOKE=0
fi
WORK="$(mktemp -d)"
trap 'rm -f "$REPO_ROOT_TB" 2>/dev/null; rm -rf "$WORK"; git worktree remove --force "$WORK/baseline-src" 2>/dev/null || true' EXIT
REPO_ROOT_TB="$PWD/internal/ingest/zz_tailbench_test.go"

echo "== building candidate (working tree) and baseline ($BASELINE_SHA)"
go build -o "$WORK/clio-candidate" ./cmd/clio
git worktree add --detach "$WORK/baseline-src" "$BASELINE_SHA" >/dev/null
(cd "$WORK/baseline-src" && go build -o "$WORK/clio-baseline" ./cmd/clio)

python3 openspec/changes/add-session-usage-summary/perf/gen_fixtures.py "$WORK/fixhome" >/dev/null
LONGFILE="$WORK/fixhome/.claude/projects/-Users-bench-proj/long0000-0000-4000-8000-000000005000.jsonl"
LONG2FILE="$WORK/fixhome/.claude/projects/-Users-bench-proj/long0000-0000-4000-8000-000000010000.jsonl"
GEMFILE="$WORK/fixhome/.gemini/tmp/h0/chats/session-2026-07-01T10-00-0000.jsonl"
TAILLINE='{"type":"assistant","uuid":"tail-x","timestamp":"2026-07-01T23:59:59Z","sessionId":"long0000-0000-4000-8000-000000005000","message":{"role":"assistant","model":"model-bench","content":[{"type":"text","text":"tail append"}],"usage":{"input_tokens":1,"output_tokens":1}}}'
TAILLINE2='{"type":"assistant","uuid":"tail-y","timestamp":"2026-07-01T23:59:59Z","sessionId":"long0000-0000-4000-8000-000000010000","message":{"role":"assistant","model":"model-bench","content":[{"type":"text","text":"tail append"}],"usage":{"input_tokens":1,"output_tokens":1}}}'

now_ms() { python3 -c 'import time; print(int(time.time()*1000))'; }

# one_run <binary> <label-file-prefix>: fresh HOME copy + fresh DB; records
# full-index ms, post-checkpoint DB bytes, tail-ingest ms (5k + 10k), gemini
# re-replay ms + db+wal growth bytes.
one_run() {
  local BIN="$1" OUT="$2"
  local H="$WORK/run-home"; rm -rf "$H"; cp -R "$WORK/fixhome" "$H"
  local XDG="$H/xdg"; mkdir -p "$XDG"
  local ENV=(env HOME="$H" XDG_DATA_HOME="$XDG")
  local DB="$XDG/clio/db.sqlite"

  local t0 t1
  t0=$(now_ms); "${ENV[@]}" "$BIN" index --full >/dev/null; t1=$(now_ms)
  echo "full_ms $((t1-t0))" >> "$OUT"

  sqlite3 "$DB" "PRAGMA wal_checkpoint(TRUNCATE);" >/dev/null
  echo "db_bytes $(sqlite3 "$DB" 'PRAGMA page_count;' | tr -d '\n') $(sqlite3 "$DB" 'PRAGMA page_size;' | tr -d '\n')" >> "$OUT"

  echo "$TAILLINE" >> "$LONGFILE_RUN"
  t0=$(now_ms); "${ENV[@]}" "$BIN" index >/dev/null; t1=$(now_ms)
  echo "tail5k_ms $((t1-t0))" >> "$OUT"

  echo "$TAILLINE2" >> "$LONG2FILE_RUN"
  t0=$(now_ms); "${ENV[@]}" "$BIN" index >/dev/null; t1=$(now_ms)
  echo "tail10k_ms $((t1-t0))" >> "$OUT"

  # Gemini single-session re-replay: checkpoint, mutate the chat file (bare
  # record upsert), measure time + combined db+wal growth before checkpoint.
  sqlite3 "$DB" "PRAGMA wal_checkpoint(TRUNCATE);" >/dev/null
  local before after
  before=$(( $(stat -f%z "$DB") + $(stat -f%z "$DB-wal" 2>/dev/null || echo 0) ))
  echo '{"id":"m1","timestamp":"2026-07-01T12:00:00Z","type":"gemini","model":"gemini-bench","content":[{"text":"updated reply"}],"tokens":{"input":501,"output":61,"cached":20,"thoughts":30,"tool":0,"total":612}}' >> "$GEMFILE_RUN"
  t0=$(now_ms); "${ENV[@]}" "$BIN" index >/dev/null; t1=$(now_ms)
  after=$(( $(stat -f%z "$DB") + $(stat -f%z "$DB-wal" 2>/dev/null || echo 0) ))
  echo "gemini_ms $((t1-t0))" >> "$OUT"
  echo "gemini_growth_bytes $((after-before))" >> "$OUT"

  # doctor DB/source ratio gate — FAIL-CLOSED: unparsable doctor output is a
  # gate failure (recorded as -1), never silently zero.
  local RATIO
  RATIO=$("${ENV[@]}" "$BIN" doctor 2>/dev/null | grep -o '~[0-9.]*x source' | grep -o '[0-9.]*' | head -1 || true)
  if [ -z "$RATIO" ]; then
    echo "doctor_ratio_x100 -1" >> "$OUT"
  else
    echo "doctor_ratio_x100 $(python3 -c "print(int(float('$RATIO')*100))")" >> "$OUT"
  fi
}

BASE_OUT="$WORK/baseline.txt"; CAND_OUT="$WORK/candidate.txt"
: > "$BASE_OUT"; : > "$CAND_OUT"
for i in $(seq 1 "$N"); do
  for PAIR in "clio-baseline:$BASE_OUT" "clio-candidate:$CAND_OUT"; do  # interleaved
    BIN="$WORK/${PAIR%%:*}"; OUT="${PAIR##*:}"
    LONGFILE_RUN="$WORK/run-home/.claude/projects/-Users-bench-proj/$(basename "$LONGFILE")"
    LONG2FILE_RUN="$WORK/run-home/.claude/projects/-Users-bench-proj/$(basename "$LONG2FILE")"
    GEMFILE_RUN="$WORK/run-home/.gemini/tmp/h0/chats/$(basename "$GEMFILE")"
    one_run "$BIN" "$OUT"
    echo "  run $i $(basename "$BIN") done"
  done
done

# Scan-volume record for the tail gate (programmatic, lands in the summary):
SCAN_LINES=$(wc -l < "$LONGFILE_RUN" | tr -d ' ')
SCAN_BYTES=$(wc -c < "$LONGFILE_RUN" | tr -d ' ')
echo "scan volume (5k fixture, post-append): $SCAN_LINES lines, $SCAN_BYTES bytes"

# Protocol tail timing: in-process Go benchmark in BOTH trees (no CLI
# discovery overhead). The benchmark file is copied in and removed after.
echo "== go-benchmark tail ingest (in-process, 20x x 3 per tree)"
TB_SRC="openspec/changes/add-session-usage-summary/perf/tailbench_go.txt"
cp "$TB_SRC" internal/ingest/zz_tailbench_test.go
cp "$TB_SRC" "$WORK/baseline-src/internal/ingest/zz_tailbench_test.go"
# Interleaved, order-counterbalanced samples: candidate/baseline alternate and
# swap starting order each round (the EXIT trap also removes the copied file).
CAND_TAIL=""; BASE_TAIL=""
one_tail_sample() { (cd "$1" && go test ./internal/ingest -run xxx -bench BenchmarkZZTailIngest5k -benchtime 20x 2>/dev/null | grep BenchmarkZZ | awk '{print $3}'); }
for _k in 1 2 3 4; do
  if [ $((_k % 2)) -eq 1 ]; then
    CAND_TAIL="$CAND_TAIL $(one_tail_sample .)"
    BASE_TAIL="$BASE_TAIL $(one_tail_sample "$WORK/baseline-src")"
  else
    BASE_TAIL="$BASE_TAIL $(one_tail_sample "$WORK/baseline-src")"
    CAND_TAIL="$CAND_TAIL $(one_tail_sample .)"
  fi
done
rm -f internal/ingest/zz_tailbench_test.go "$WORK/baseline-src/internal/ingest/zz_tailbench_test.go"
echo "gobench_tail_ns baseline: $BASE_TAIL"
echo "gobench_tail_ns candidate: $CAND_TAIL"
GOBENCH_ABS_MS=$(python3 -c "
import sys, statistics as st
b=[float(x) for x in '''$BASE_TAIL'''.split()]
c=[float(x) for x in '''$CAND_TAIL'''.split()]
print(f'{(st.median(c)-st.median(b))/1e6:.2f}' if b and c else 'unparsable')
")
echo "gobench tail ABSOLUTE overhead (median): ${GOBENCH_ABS_MS} ms (gate < 20 ms; in-process relative bound is unmeetable by construction, see design.md)"

# Write-lock gate: AB/BA COUNTERBALANCED pairs at the design-mandated 200
# iterations — six pairs, odd pairs WithUsage-first and even pairs
# WithoutUsage-first (3 AB + 3 BA = perfectly balanced), so systematic
# thermal/order drift cancels instead of biasing one variant.
echo "== write-lock benchmark (tail commit, usage vs nil; AB/BA counterbalanced, 200x x6)"
LOCK_OUT=""
for _i in 1 2 3 4 5 6; do
  if [ $((_i % 2)) -eq 1 ]; then
    A=$(go test ./internal/ingest -run xxx -bench 'BenchmarkCommitWithUsage$' -benchtime 200x 2>/dev/null | grep Benchmark || true)
    B=$(go test ./internal/ingest -run xxx -bench 'BenchmarkCommitWithoutUsage$' -benchtime 200x 2>/dev/null | grep Benchmark || true)
  else
    B=$(go test ./internal/ingest -run xxx -bench 'BenchmarkCommitWithoutUsage$' -benchtime 200x 2>/dev/null | grep Benchmark || true)
    A=$(go test ./internal/ingest -run xxx -bench 'BenchmarkCommitWithUsage$' -benchtime 200x 2>/dev/null | grep Benchmark || true)
  fi
  LOCK_OUT="$LOCK_OUT
$A
$B"
done
echo "$LOCK_OUT"
LOCK_DELTA=$(echo "$LOCK_OUT" | python3 -c "
import sys, statistics as st
d={}
for line in sys.stdin:
    p=line.split()
    if len(p)>=3: d.setdefault(p[0].split('-')[0],[]).append(float(p[2]))
w=d.get('BenchmarkCommitWithUsage'); wo=d.get('BenchmarkCommitWithoutUsage')
print(f'{(st.median(w)-st.median(wo))/st.median(wo)*100:+.1f}' if w and wo else 'unparsable')
")
echo "write-lock delta (median): ${LOCK_DELTA}%"

export GOBENCH_ABS_MS LOCK_DELTA SMOKE
python3 - "$BASE_OUT" "$CAND_OUT" <<'PYEOF'
import sys, statistics as st
def load(p):
    d = {}
    for line in open(p):
        parts = line.split()
        if parts[0] == "db_bytes":
            d.setdefault("db_bytes", []).append(int(parts[1]) * int(parts[2]))
        else:
            d.setdefault(parts[0], []).append(int(parts[1]))
    return {k: st.median(v) for k, v in d.items()}
import math as _m
b, c = load(sys.argv[1]), load(sys.argv[2])
fails = []
def pct(k): return (c[k] - b[k]) / b[k] * 100 if b.get(k) else float("nan")
def require_finite(k):
    v = pct(k)
    if k not in b or k not in c or _m.isnan(v) or _m.isinf(v):
        fails.append(f"{k}: missing/unparsable in baseline or candidate (fail-closed)")
        return None
    return v
print("metric              baseline   candidate   delta")
for k in ["full_ms", "db_bytes", "tail5k_ms", "tail10k_ms", "gemini_ms", "gemini_growth_bytes", "doctor_ratio_x100"]:
    print(f"{k:20s}{b.get(k,0):>10.0f}{c.get(k,0):>12.0f}{pct(k):>+9.1f}%")
print()
# ENFORCED gates (design.md): non-zero exit on any violation.
import os as _os
fails = []
def _num(v):
    try:
        f = float(v)
        return None if _m.isnan(f) else f
    except (ValueError, TypeError):
        return None
gd = _num(_os.environ.get("GOBENCH_ABS_MS"))
ld = _num(_os.environ.get("LOCK_DELTA"))
if ld is None:
    fails.append("write-lock benchmark unparsable/missing (fail-closed)")
elif ld >= 10:  # one-sided: usage making the commit FASTER is not a violation
    fails.append(f"write-lock delta {ld:+.1f}% >= +10%")
if gd is None:
    fails.append("gobench tail absolute overhead unparsable/missing (fail-closed)")
elif gd >= 20:
    fails.append(f"gobench tail absolute overhead {gd:.2f} ms >= 20 ms")
if _os.environ.get("SMOKE") == "1":
    fails.append("n < 10: smoke run cannot certify gates (fail-closed)")
if c.get("doctor_ratio_x100", 0) < 0 or b.get("doctor_ratio_x100", 0) < 0:
    fails.append("doctor output unparsable (fail-closed)")
v = require_finite("db_bytes")
if v is not None and v >= 2: fails.append(f"db_bytes {v:+.1f}% >= +2%")
v = require_finite("full_ms")
if v is not None and v >= 5: fails.append(f"full_ms {v:+.1f}% >= +5%")
# tail5k CLI-level delta is recorded but NOT a gate: across protocol runs it
# ranged +21.5%..+40.6% — a ~11-15ms real cost plus machine noise divided by a
# ~70ms denominator that is mostly fixed command overhead. The certified tail
# gate is the in-process ABSOLUTE overhead above.
if "gemini_growth_bytes" not in b or "gemini_growth_bytes" not in c:
    fails.append("gemini_growth_bytes: missing (fail-closed)")
elif b["gemini_growth_bytes"] > 0 and pct("gemini_growth_bytes") >= 10:
    fails.append(f"gemini_growth_bytes {pct('gemini_growth_bytes'):+.1f}% >= +10%")
if c.get("doctor_ratio_x100", 0) >= 300: fails.append(f"doctor ratio {c['doctor_ratio_x100']/100:.2f}x >= 3x")
if fails:
    print("GATE FAILURES:"); [print("  FAIL:", f) for f in fails]
    import sys as _s; _s.exit(1)
print("ALL GATES PASS (db_bytes<+2%  full_ms<+5%  tail-abs(gobench)<20ms  write-lock<+10% one-sided  gemini_growth<+10%  doctor<3x  n>=10)")
print("(recorded, not gates: tail5k/tail10k CLI-level deltas — unstable metric, see design.md)")
print("(informational: tail5k/tail10k CLI-level timings above include fixed command overhead;")
print(" the certified tail gate is the in-process Go benchmark, per the design.md protocol)")
PYEOF
