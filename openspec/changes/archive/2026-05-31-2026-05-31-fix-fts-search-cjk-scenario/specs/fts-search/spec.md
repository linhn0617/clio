## MODIFIED Requirements

### Requirement: Full-text search with trigram tokenizer

The system SHALL provide full-text search over indexed messages using an FTS5
trigram tokenizer for query terms of 3+ runes. Queries whose terms are all shorter
than 3 runes use the substring fallback instead (see "Hybrid short/long term search").

#### Scenario: CJK trigram match (3+ runes)

- **WHEN** the user searches for a 3+ rune CJK query such as `資料驗`
- **THEN** the system SHALL return messages containing it as a sub-sequence (e.g.
  `資料驗證流程`), because the trigram tokenizer matches 3-gram sub-sequences

#### Scenario: Short CJK query uses the substring fallback

- **WHEN** the user searches for a 2-rune CJK query such as `驗證`
- **THEN** the system SHALL match it via the escaped substring scan — the trigram
  index needs 3+ runes — still returning messages containing `資料驗證流程`

#### Scenario: Code identifier match

- **WHEN** the user searches for a camelCase or snake_case identifier
- **THEN** the system SHALL match occurrences inside code blocks and tool output
