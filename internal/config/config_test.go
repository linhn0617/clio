package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDataDirIgnoresRelativeXDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "relative/dir")
	dir, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dir, "relative/dir") {
		t.Fatalf("relative XDG_DATA_HOME must be ignored, got %q", dir)
	}
}

func TestGeminiTmpDir(t *testing.T) {
	dir, err := GeminiTmpDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(dir, filepath.Join(".gemini", "tmp")) {
		t.Fatalf("GeminiTmpDir() = %q, want a path ending in .gemini/tmp", dir)
	}
}

func TestDataDirHonorsAbsoluteXDG(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_DATA_HOME", abs)
	dir, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(abs, "clio") {
		t.Fatalf("absolute XDG_DATA_HOME not honored: got %q want %q", dir, filepath.Join(abs, "clio"))
	}
}
