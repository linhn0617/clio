## 1. Fields outside the rows over the budget

- [x] 1.1 Add failing tests: `read_session` with a 6,000-character project path and
      `ask` with an oversized question at `max_chars: 4000` must fit, cut the long
      field with a marker, keep the uuid whole and still return rows.
- [x] 1.2 Cut the fields outside the rows to half the budget before rows are
      dropped, re-applied on every render.
- [x] 1.3 Replace a string only when the cut is shorter as sent, so size is
      monotone in the cap.
- [x] 1.4 Keep ids, notes and hints whole; keep key order and exact numbers.
- [x] 1.5 Name the cut fields in `next`; keep render's own reasons.
- [x] 1.6 Cut to a quarter before the subagent shed, measuring the non-list fields
      alone; the shed measures with the cut applied (`zeroSize`); keep
      top-level notes and `parent_session` whole (any key case); write uncut tokens
      back byte for byte.
- [x] 1.7 Mutation-check: no cut step, cut after the shed, cutting ids, top-level
      key, key case, re-encoding uncut strings, cutting regardless of length, lists
      included, shed measuring the uncut render, half instead of a quarter — each
      turns a named test red.
- [x] 1.8 Update the mcp-server requirement and add the scenario.

## 2. Verification

- [x] 2.1 gofmt, go vet, go test ./..., openspec validate --all --strict.
