package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

const fingerprintWindow = 512

// maxLineBytes caps the in-memory size of a single line during streaming ingest.
// A line that reaches this without a newline is treated as unparseable (counted and
// discarded once its newline is seen), so a giant or newline-less line cannot OOM.
const maxLineBytes = 16 << 20 // 16 MiB

// headFingerprint hashes the leading fingerprintWindow bytes. Validated alongside the
// tail fingerprint to catch a rewrite that changed the start of an (otherwise
// append-looking) file. Returns "" for an empty file.
func headFingerprint(f *os.File) (string, error) {
	buf := make([]byte, fingerprintWindow)
	n, err := f.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	sum := sha256.Sum256(buf[:n])
	return hex.EncodeToString(sum[:]), nil
}

// FileState mirrors a row of the ingest_state table.
type FileState struct {
	SourceFile      string
	LastSize        int64
	LastMTime       int64
	LastByteOffset  int64
	TailFingerprint string
	HeadFingerprint string
	LastIngestedAt  int64
	UnparsedLines   int64
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
	case size > prior.LastSize:
		return changeIncremental // grew: an append
	case mtime != prior.LastMTime:
		return changeFull // same size, new mtime: a rewrite, never an append
	default:
		return changeSkip // same size, same mtime
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
