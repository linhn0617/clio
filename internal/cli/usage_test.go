package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/linhn0617/clio/internal/sessions"
)

func TestQuotaStale(t *testing.T) {
	now := time.Now()
	fresh := sessions.QuotaSnapshotRow{ObservedAt: now.Add(-time.Hour).Unix(), WindowMinutes: 10080, ResetsAt: now.Add(24 * time.Hour).Unix()}
	if quotaStale(fresh, now) {
		t.Fatal("fresh snapshot must not be stale")
	}
	windowExceeded := sessions.QuotaSnapshotRow{ObservedAt: now.Add(-8 * 24 * time.Hour).Unix(), WindowMinutes: 10080, ResetsAt: now.Add(24 * time.Hour).Unix()}
	if !quotaStale(windowExceeded, now) {
		t.Fatal("snapshot older than its window must be stale")
	}
	resetPassed := sessions.QuotaSnapshotRow{ObservedAt: now.Add(-time.Hour).Unix(), WindowMinutes: 10080, ResetsAt: now.Add(-time.Minute).Unix()}
	if !quotaStale(resetPassed, now) {
		t.Fatal("snapshot whose reset time passed must be stale")
	}
}

func TestHumanTokens(t *testing.T) {
	cases := map[int64]string{999: "999", 1500: "1.5k", 34000: "34k", 1_200_000: "1.2M", 2_500_000_000: "2.5B"}
	for n, want := range cases {
		if got := humanTokens(n); got != want {
			t.Fatalf("humanTokens(%d)=%q want %q", n, got, want)
		}
	}
}

func TestUsageSuffixPlaceholderNotZero(t *testing.T) {
	totals := map[string]int64{"a": 1500}
	stale := map[string]bool{"a": true}
	if got := usageSuffix(totals, stale, "a"); got != "  [1.5k tok, stale]" {
		t.Fatalf("suffix=%q", got)
	}
	if got := usageSuffix(totals, stale, "missing"); got != "" {
		t.Fatalf("absent session must render empty suffix (placeholder), got %q", got)
	}
}

func TestFormatUsageSessionRowCarriesCategoriesAndMarkers(t *testing.T) {
	r := sessions.UsageSessionRow{
		SessionUUID: "abcdef1234567890", Source: "claude-code", ProjectPath: "/p",
		Title: "fix the parser", AgentType: "general-purpose",
		InputTokens: 1000, OutputTokens: 200, CacheRead: 50, CacheCreation: 25,
		Reasoning: 10, Tool: 5, TotalTokens: 1275, Stale: true,
	}
	line := formatUsageSessionRow(r)
	for _, want := range []string{"in 1.0k", "out 200", "cr 50", "cc 25", "rsn 10", "tool 5", "[stale]", "↳general-purpose", "fix the parser"} {
		if !strings.Contains(line, want) {
			t.Fatalf("row %q missing %q", line, want)
		}
	}
}

func TestFormatUsageGroupRowDrillDownPerRow(t *testing.T) {
	proj := formatUsageGroupRow("project", sessions.UsageGroupRow{Source: "codex", Key: "/Users/x/proj", Sessions: 3, TotalTokens: 5000, StaleSessions: 2})
	if !strings.Contains(proj, `clio usage --project '/Users/x/proj' --by session --source codex`) {
		t.Fatalf("project row lacks its own drill-down: %q", proj)
	}
	if !strings.Contains(proj, "[stale: 2 sessions]") {
		t.Fatalf("project row lacks stale count: %q", proj)
	}
	mdl := formatUsageGroupRow("model", sessions.UsageGroupRow{Source: "gemini", Key: "gemini-x", Sessions: 1, TotalTokens: 10})
	if !strings.Contains(mdl, `clio usage --model 'gemini-x' --by session --source gemini`) {
		t.Fatalf("model row lacks model-selecting drill-down: %q", mdl)
	}
}

func TestShellQuoteNeutralizesSubstitution(t *testing.T) {
	q := shellQuote("$(rm -rf ~)/`evil`/it's")
	if !strings.HasPrefix(q, "'") || !strings.HasSuffix(q, "'") {
		t.Fatalf("not single-quoted: %q", q)
	}
	if strings.Contains(q, `"`) {
		t.Fatalf("double quotes allow substitution: %q", q)
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Fatalf("embedded quote escaping wrong: %q", got)
	}
}
