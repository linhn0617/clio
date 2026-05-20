package mcp_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestStdioPurityE2E builds the real binary and drives it over stdio in an
// isolated HOME, asserting that stdout carries only JSON-RPC (logs go to stderr).
func TestStdioPurityE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess E2E in -short mode")
	}

	home := t.TempDir()
	projDir := filepath.Join(home, ".claude", "projects", "-Users-x-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	events := strings.Join([]string{
		`{"type":"user","timestamp":"2026-05-01T10:00:00Z","cwd":"/Users/x/proj","sessionId":"sess-1","message":{"role":"user","content":"how do we handle authentication flow"}}`,
		`{"type":"assistant","timestamp":"2026-05-01T10:00:01Z","sessionId":"sess-1","message":{"role":"assistant","content":[{"type":"text","text":"use a middleware"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(projDir, "sess-1.jsonl"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(t.TempDir(), "clio")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/clio")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search","arguments":{"query":"authentication"}}}`,
	}, "\n") + "\n"

	cmd := exec.Command(bin, "mcp")
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_DATA_HOME="+filepath.Join(home, "data"),
	)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("clio mcp failed: %v\nstderr: %s", err, stderr.String())
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected >=3 JSON-RPC responses, got %d:\n%s", len(lines), stdout.String())
	}
	var sawSearchHit bool
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(l), &obj); err != nil {
			t.Fatalf("non-JSON line on stdout (purity violated): %q", l)
		}
		if obj["id"] == float64(3) {
			// The session must appear in the search result (snippet text may be
			// trimmed at trigram boundaries, so match on the session id).
			sawSearchHit = strings.Contains(l, "sess-1")
		}
	}
	if !sawSearchHit {
		t.Fatalf("search response missing expected hit.\nstdout:\n%s", stdout.String())
	}
}
