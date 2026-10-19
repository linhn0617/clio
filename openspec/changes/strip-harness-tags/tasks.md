## 1. Shapes

- [x] 1.1 Grep the real transcript export for each harness shape (task-notification,
      system-reminder, agent-message with its preamble and trailer, SYSTEM NOTIFICATION,
      recommended_plugins, Subagent hand-back, local-command-stdout) and record which
      are exact enough to strip.
- [x] 1.2 Check Codex transcripts and session titles for the same shapes.

## 2. Red-first tests

- [x] 2.1 Fixture-style tests per shape with synthetic content, a prompt with an
      appended reminder, a harness-only message, raw_json unchanged, unclosed and
      malformed tags, prose mentions, user-role only, redaction and `<private>` order.
- [x] 2.2 A linear-time test on adversarial input under the existing 500 ms ceiling.
- [x] 2.3 An end-to-end ingest test: harness text not searchable, the prompt still is.

## 3. Implementation

- [x] 3.1 Add `stripHarnessWrappers` (line-anchored, strictly nested, linear) and apply
      it to user-role text after redaction and to the title hint.

## 4. Verification

- [x] 4.1 Run negative controls (strip before redaction, unanchored opener, quadratic
      closer search, loose nesting) and revert each.
- [x] 4.2 Run gofmt, go vet, all tests and the ingest race tests.
- [x] 4.3 Document `clio index --full` in the CHANGELOG and README.
- [x] 4.4 Run openspec validate strip-harness-tags --strict with telemetry disabled.
