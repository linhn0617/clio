## 1. Fixed fields over the budget

- [x] 1.1 Add a failing test: `read_session` with a 6,000-character project path and
      `ask` with an oversized question at `max_chars: 4000` must fit, cut the long
      field with a marker and keep the uuid whole.
- [x] 1.2 Add the field-capping fallback step to `fitBudget`.
- [x] 1.3 Mutation-check: skipping nested objects turns the test red.
- [x] 1.4 Update the mcp-server requirement and add the scenario.
- [x] 1.5 Keep render's own `next` reasons (read_session subagent note) in the cut.
- [x] 1.6 Replace a string only when the cut is shorter as sent, so size is
      monotone in the cap; test monotonicity and short fields staying whole.

## 2. Verification

- [x] 2.1 gofmt, go vet, go test ./..., openspec validate --all --strict.
