package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

const fingerprintWindow = 512

// FileState mirrors a row of the ingest_state table.
type FileState struct {
	SourceFile      string
	LastSize        int64
	LastMTime       int64
	LastByteOffset  int64
	TailFingerprint string
	LastIngestedAt  int64
}

type changeKind int

const (
	changeSkip        changeKind = iota // unchanged, nothing to do
	changeIncremental                   // append-only growth, read from offset
	changeFull                          // rewritten/truncated, re-ingest whole file
)

// classifyChange decides what to do based on cheap stat data alone. When it
// returns changeIncremental or (same-size, different-mtime) the caller must
// still verify the tail fingerprint before trusting the stored offset.
func classifyChange(prior *FileState, size, mtime int64) changeKind {
	if prior == nil {
		return changeFull // never seen
	}
	switch {
	case size < prior.LastSize:
		return changeFull // truncated or rewritten smaller
	case size == prior.LastSize && mtime == prior.LastMTime:
		return changeSkip
	default:
		return changeIncremental // grew, or same size with new mtime
	}
}

// fingerprintAt hashes the up-to-fingerprintWindow bytes ending at offset. Used
// to detect that the bytes preceding the stored offset still match — i.e. the
// file is a true append, not a same-size rewrite or atomic replace.
func fingerprintAt(f *os.File, offset int64) (string, error) {
	if offset <= 0 {
		return "", nil
	}
	start := max(offset-fingerprintWindow, 0)
	buf := make([]byte, offset-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return "", err
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

// lastCompleteNewline returns the offset just past the final '\n' in buf, so
// callers ingest only complete lines and leave any partial trailing line for a
// later run. Returns 0 if buf has no newline.
func lastCompleteNewline(buf []byte) int {
	for i := len(buf) - 1; i >= 0; i-- {
		if buf[i] == '\n' {
			return i + 1
		}
	}
	return 0
}
