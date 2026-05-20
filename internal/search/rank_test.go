package search

import (
	"testing"
	"time"
)

func TestAdjustedScoreRoleWeighting(t *testing.T) {
	// Same bm25 and ts: dialogue must outrank tool output.
	ts := time.Now().Unix()
	user := adjustedScore(-2.0, "user", ts)
	tool := adjustedScore(-2.0, "tool_result", ts)
	if !(user > tool) {
		t.Fatalf("expected user(%.3f) > tool_result(%.3f)", user, tool)
	}
}

func TestAdjustedScoreRecency(t *testing.T) {
	now := time.Now().Unix()
	old := time.Now().Add(-365 * 24 * time.Hour).Unix()
	recent := adjustedScore(-2.0, "user", now)
	stale := adjustedScore(-2.0, "user", old)
	if !(recent > stale) {
		t.Fatalf("expected recent(%.3f) > stale(%.3f)", recent, stale)
	}
}

func TestRecencyBonusZeroTS(t *testing.T) {
	if recencyBonus(0) != 0 {
		t.Fatal("expected 0 bonus for unknown ts")
	}
}
