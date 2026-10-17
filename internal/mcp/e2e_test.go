package mcp_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStdioPurityE2E builds the real binary and drives it over stdio in an
// isolated HOME, asserting that stdout carries only JSON-RPC (logs go to stderr)
// and that mcp's own startup catch-up makes existing history searchable.
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

	cmd := exec.Command(bin, "mcp")
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_DATA_HOME="+filepath.Join(home, "data"),
	)
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

	// Drive the server like a real client and keep stdin open until the session is
	// found (mcp-go >= 1.0 cancels in-flight requests once stdin hits EOF). The server
	// serves immediately and ingests in the background, so early searches may come
	// back empty: poll until the startup catch-up has made the data searchable.
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(stdoutPipe)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	send := func(s string) {
		if _, err := io.WriteString(stdin, s+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)

	nextID := 3
	sendSearch := func() {
		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"search","arguments":{"query":"authentication"}}}`, nextID))
		nextID++
	}
	sendSearch()

	var stdout bytes.Buffer
	var sawInit, sawList, sawSearchHit bool
	deadline := time.After(30 * time.Second)
	retry := time.NewTicker(300 * time.Millisecond)
	defer retry.Stop()
	for !(sawInit && sawList && sawSearchHit) {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("server closed stdout early.\nstdout:\n%s\nstderr: %s", stdout.String(), stderr.String())
			}
			stdout.WriteString(l + "\n")
			if strings.TrimSpace(l) == "" {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(l), &obj); err != nil {
				t.Fatalf("non-JSON line on stdout (purity violated): %q", l)
			}
			id, _ := obj["id"].(float64)
			switch {
			case id == 1:
				sawInit = true
			case id == 2:
				sawList = true
			case id >= 3:
				// The session must appear in the search result (snippet text may be
				// trimmed at trigram boundaries, so match on the session id).
				if strings.Contains(l, "sess-1") {
					sawSearchHit = true
				}
			}
		case <-retry.C:
			if !sawSearchHit && nextID < 200 {
				sendSearch()
			}
		case <-deadline:
			_ = cmd.Process.Kill()
			t.Fatalf("search never found sess-1 within 30s.\nstdout:\n%s\nstderr: %s", stdout.String(), stderr.String())
		}
	}
	_ = stdin.Close()
	// Shutdown must be prompt too: drain until stdout closes, then wait for exit,
	// both under a deadline so a hung server fails the test instead of the suite.
	exitDeadline := time.After(30 * time.Second)
	dump := func() string {
		return fmt.Sprintf("stdout:\n%s\nstderr: %s", stdout.String(), stderr.String())
	}
drain:
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				break drain
			}
			stdout.WriteString(l + "\n")
			if strings.TrimSpace(l) == "" {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(l), &obj); err != nil {
				t.Fatalf("non-JSON line on stdout (purity violated): %q", l)
			}
		case <-exitDeadline:
			_ = cmd.Process.Kill()
			t.Fatalf("server did not close stdout within 30s of stdin EOF.\n%s", dump())
		}
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("clio mcp exited with error: %v\n%s", err, dump())
		}
	case <-exitDeadline:
		_ = cmd.Process.Kill()
		t.Fatalf("server did not exit within 30s of stdin EOF.\n%s", dump())
	}
}
