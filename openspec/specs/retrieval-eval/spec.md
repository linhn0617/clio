# retrieval-eval Specification

## Purpose
TBD - created by archiving change 2026-07-14-retrieval-eval-and-ask-budget. Update Purpose after archive.
## Requirements
### Requirement: Deterministic hand-written fixture corpus

The retrieval regression suite SHALL run over a small (approximately 10–12 sessions),
hand-written, sanitized fixture corpus committed in the repository, NOT derived from any
real or private conversation history. The corpus SHALL cover the query classes the
ranking stack distinguishes: English-only, CJK-only (Traditional Chinese), and mixed
EN/CJK content; code-fragment content; quoted-phrase targets; and content exercising the
FTS-versus-LIKE tier split. It SHALL be loaded into a temporary database at test time,
with message timestamps assigned as fixed relative offsets from load time so that
recency-dependent ranking produces a stable ordering across runs.

#### Scenario: The corpus is loaded reproducibly

- **WHEN** the regression suite runs
- **THEN** it SHALL load the committed corpus into a temporary database and produce the
  same rankings on repeated runs

#### Scenario: No private history is used

- **WHEN** the fixture corpus is inspected
- **THEN** it SHALL contain only synthetic, sanitized content, with no data sourced from
  a real user's history

### Requirement: Assertion-based regression queries, Search and Ask asserted separately

The suite SHALL define separate binary expectation sets for `search.Search` and
`ask.Ask`. Each query SHALL declare the items expected in the top-k: Search expectations
SHALL be keyed on `(session, seq)` message hits appearing in the top-k results, and Ask
expectations SHALL be keyed on `session` appearing among the top-k groups, optionally
with an expected hit sequence that must be marked as a hit. The query sets SHALL include
English, CJK, mixed, code-fragment, and quoted-phrase queries. A failed assertion SHALL
produce a self-explanatory message naming the query, the expectation, and the actual
top-k results. The suite SHALL NOT use graded relevance or aggregate quality metrics.

#### Scenario: Search and Ask use distinct expectation sets

- **WHEN** the suite runs
- **THEN** it SHALL assert `search.Search` against the Search expectations and `ask.Ask`
  against the Ask expectations, without conflating the two

#### Scenario: A dropped expected hit fails with a diagnosable message

- **WHEN** a change causes an expected item to fall out of its declared top-k
- **THEN** the suite SHALL fail with a message naming the query, the expected item, and
  the actual top-k results

### Requirement: Snippet visibility and ask bundle budget are asserted

For each expected hit that is returned, the suite SHALL assert that its returned snippet
or hit excerpt contains at least one extracted query term (snippet visibility). For each
Ask query, the suite SHALL assert — using the same token estimator the `ask` capability
uses for enforcement — that the returned bundle's estimated token size is within its
effective budget: the larger of the configured budget and the top group's minimum-length
hit excerpts, so that correct keep-top-hits invariant behavior is never reported as a
failure. Suite budgets SHOULD be set comfortably above that floor. Per-query-set latency
SHALL be reported for visibility but SHALL NOT be a pass/fail assertion (it is
machine-dependent).

#### Scenario: An invisible match fails the suite

- **WHEN** a returned expected hit's snippet or excerpt contains none of the extracted
  query terms
- **THEN** the suite SHALL fail with a message naming the query and the returned snippet

#### Scenario: An over-budget ask bundle fails the suite

- **WHEN** an `ask` bundle produced during the suite exceeds its effective budget
- **THEN** the suite SHALL fail

#### Scenario: Invariant-driven overage is not a failure

- **WHEN** a bundle exceeds the configured budget only because the keep-top-hits
  invariant emitted the top group's minimum-length hit excerpts
- **THEN** the suite SHALL NOT report a failure

### Requirement: The suite runs under the default go test

The regression suite SHALL be an ordinary Go test package requiring no build tag or
dedicated command: it SHALL run as part of `go test ./...` and therefore be exercised by
the existing continuous-integration test step with no workflow change. Corpus loading
SHALL be fast enough (a small fixture set into a temporary database) not to burden the
default test run.

#### Scenario: A regression fails the default build

- **WHEN** a change breaks a suite expectation and `go test ./...` runs
- **THEN** the build SHALL fail without any extra tag or flag

#### Scenario: CI needs no new step

- **WHEN** the existing CI test step (`go test -race ./...`) runs
- **THEN** the regression suite SHALL be included in it

