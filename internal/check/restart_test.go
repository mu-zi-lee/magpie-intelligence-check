package check

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRestartRestoresProtection(t *testing.T) {
	dir := t.TempDir()
	gate := filepath.Join(dir, "gate.js")
	s, err := New(dir, gate)
	if err != nil {
		t.Fatal(err)
	}
	s.SetTarget("a/solo", true)
	s.Action("a/solo", "hold")
	s.Action("a/solo", "allow")
	s.mu.Lock()
	s.state.Targets["a/solo"].AllowUntil = time.Now().Add(-time.Minute)
	s.saveLocked()
	s.mu.Unlock()
	s.Close()
	restored, err := New(dir, gate)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	state, _ := restored.Snapshot()
	if !state.Targets["a/solo"].Held || !state.Targets["a/solo"].Enabled {
		t.Fatal("restart removed protection")
	}
}
