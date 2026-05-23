// Package watcher live-ingests Claude Code session files while the MCP server
// runs. fsnotify is not recursive, so subdirectories are watched explicitly and
// new project directories are picked up via Create events; a periodic full walk
// backstops any dropped events.
package watcher

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/linhn0617/clio/internal/ingest"
)

const (
	debounceWindow = 500 * time.Millisecond
	backstopPeriod = 60 * time.Second
)

// Watcher watches projectsDir and incrementally ingests changes.
type Watcher struct {
	ing         *ingest.Ingester
	projectsDir string
	log         *slog.Logger
}

// New creates a Watcher.
func New(ing *ingest.Ingester, projectsDir string, log *slog.Logger) *Watcher {
	return &Watcher{ing: ing, projectsDir: projectsDir, log: log}
}

// Run watches until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fsw.Close()

	w.addDirsRecursive(fsw, w.projectsDir)

	dirty := map[string]struct{}{}
	var debounce <-chan time.Time
	backstop := time.NewTicker(backstopPeriod)
	defer backstop.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			w.handleEvent(fsw, ev, dirty)
			if len(dirty) > 0 && debounce == nil {
				debounce = time.After(debounceWindow)
			}

		case <-debounce:
			debounce = nil
			for path := range dirty {
				if ctx.Err() != nil {
					return nil
				}
				if _, _, err := w.ing.IngestFile(ctx, path, false); err != nil {
					w.log.Warn("watch ingest failed", "file", path, "err", err)
				}
				delete(dirty, path)
			}

		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			w.log.Warn("watcher error", "err", err)

		case <-backstop.C:
			// Recover any events fsnotify dropped, and pick up new dirs.
			w.addDirsRecursive(fsw, w.projectsDir)
			if _, err := w.ing.IngestAll(ctx, w.projectsDir, false); err != nil {
				w.log.Warn("backstop ingest failed", "err", err)
			}
		}
	}
}

func (w *Watcher) handleEvent(fsw *fsnotify.Watcher, ev fsnotify.Event, dirty map[string]struct{}) {
	if ev.Op&(fsnotify.Create|fsnotify.Write) != 0 {
		fi, err := os.Stat(ev.Name)
		if err == nil && fi.IsDir() {
			// New project directory: watch it and ingest anything already inside.
			w.addDirsRecursive(fsw, ev.Name)
			if files, err := ingest.WalkSessionFiles(ev.Name); err == nil {
				for _, f := range files {
					dirty[f] = struct{}{}
				}
			}
			return
		}
		if strings.HasSuffix(ev.Name, ".jsonl") {
			dirty[ev.Name] = struct{}{}
		}
	}
}

func (w *Watcher) addDirsRecursive(fsw *fsnotify.Watcher, root string) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// fsnotify dedupes repeated Add calls, so re-adding is safe.
			if addErr := fsw.Add(path); addErr != nil {
				w.log.Warn("watch add failed", "dir", path, "err", addErr)
			}
		}
		return nil
	})
}
