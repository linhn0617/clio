package mcp_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	// Drive the server like a real client: keep stdin open until every
	// response has arrived. mcp-go >= 1.0 runs tool calls on a worker pool
	// and cancels in-flight requests as soon as stdin hits EOF, so feeding
	// the whole script from a closed reader makes the search call fail with
	// "context canceled" before it can answer.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("clio mcp failed to start: %v", err)
	}
	if _, err := io.WriteString(stdin, input); err != nil {
		t.Fatal(err)
	}

	// Collect stdout lines until the search response (id 3) shows up, with a
	// deadline so a hung server fails the test instead of the suite.
	var stdout bytes.Buffer
	var lines []string
	done := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdoutPipe)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for sc.Scan() {
			l := sc.Text()
			stdout.WriteString(l + "\n")
			lines = append(lines, l)
			if strings.Contains(l, `"id":3`) {
				break
			}
		}
		done <- sc.Err()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("reading stdout: %v\nstderr: %s", err, stderr.String())
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("timed out waiting for the search response.\nstdout:\n%s\nstderr: %s", stdout.String(), stderr.String())
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("clio mcp exited with error: %v\nstderr: %s", err, stderr.String())
	}

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
