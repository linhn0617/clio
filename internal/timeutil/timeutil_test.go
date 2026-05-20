package timeutil

import (
	"testing"
	"time"
)

func TestParseSinceRelative(t *testing.T) {
	now := time.Now().Unix()
	got, err := ParseSince("1d")
	if err != nil {
		t.Fatal(err)
	}
	if got > now-86000 || got < now-87000 {
		t.Errorf("1d => %d out of range (now %d)", got, now)
	}
	if h, _ := ParseSince("2h"); h > now-7000 || h < now-7400 {
		t.Errorf("2h => %d out of range", h)
	}
}

func TestParseSinceEmpty(t *testing.T) {
	if ts, err := ParseSince(""); ts != 0 || err != nil {
		t.Fatalf("empty => %d, %v want 0, nil", ts, err)
	}
}

func TestParseSinceAbsolute(t *testing.T) {
	ts, err := ParseSince("2026-01-15")
	if err != nil || ts == 0 {
		t.Fatalf("absolute date failed: %d %v", ts, err)
	}
}

func TestParseSinceInvalid(t *testing.T) {
	if _, err := ParseSince("nonsense"); err == nil {
		t.Fatal("expected error for invalid input")
	}
}
