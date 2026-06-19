// Command clio-ingest-once is a test-only helper: it opens the DB at $CLIO_DB and
// runs IngestAll over $CLIO_PROJECTS once, then exits. Used by the cross-process
// concurrency test to spawn real competing writers.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/ingest"
)

func main() {
	dbPath := os.Getenv("CLIO_DB")
	projects := os.Getenv("CLIO_PROJECTS")
	d, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer d.Close()
	ing := ingest.New(d, nil)
	ing.AddCodexSource() // ingest Codex CLI history too, when installed
	if _, err := ing.IngestAll(context.Background(), projects, false); err != nil {
		fmt.Fprintln(os.Stderr, "ingest:", err)
		os.Exit(1)
	}
}
