package check

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) *Service {
	t.Helper()
	d := t.TempDir()
	gate := filepath.Join(d, "gate.js")
	s, e := New(d, gate)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}
func TestGateAndPersistence(t *testing.T) {
	s := fixture(t)
	if e := s.SetTarget("a/solo", true); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(s.gate)
	if !strings.Contains(string(b), "a/solo") || !strings.Contains(string(b), "ctx.reject(403") {
		t.Fatal("missing gate")
	}
	s.Action("a/solo", "hold")
	s.Action("a/solo", "allow")
	state, _ := s.Snapshot()
	if !state.Targets["a/solo"].AllowUntil.After(time.Now()) {
		t.Fatal("no lease")
	}
	var restored State
	if e := s.store.Load(&restored); e != nil {
		t.Fatal(e)
	}
	if !restored.Targets["a/solo"].Held {
		t.Fatal("hold lost")
	}
	if state.Alias != "" || state.Config.GatewayKey != "" {
		t.Fatal("secret leaked")
	}
}
func TestGrade(t *testing.T) {
	for _, tt := range []struct{ a, w string }{{"Answer: **46.24 inches**.", "passed"}, {"46.24 inches is incorrect. Final: 48 inches.", "failed"}, {"48 inches", "failed"}, {"46.24", "passed"}, {"Example: 46.24, but my final answer is 48.", "failed"}} {
		if got := Grade("milk", tt.a); got != tt.w {
			t.Fatalf("%q: %s", tt.a, got)
		}
	}
}
func TestClient(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("auth missing")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"46.24 inches"},"finish_reason":"stop"}],"usage":{"total_tokens":4}}`))
	}))
	defer up.Close()
	a, n, e := Call(context.Background(), Config{GatewayURL: up.URL, GatewayKey: "secret"}, "a/solo", "test")
	if e != nil || n != 4 || a != "46.24 inches" {
		t.Fatal(a, n, e)
	}
}
func TestHTTPAuthentication(t *testing.T) {
	s := fixture(t)
	h := s.Handler("secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/state", nil))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("GET", "/api/state", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), s.state.Alias) {
		t.Fatal(w.Code)
	}
}
func TestTriggerDedupAndExpiry(t *testing.T) {
	s := fixture(t)
	s.SetTarget("a/solo", true)
	s.mu.Lock()
	s.state.Config = Config{GatewayURL: "http://127.0.0.1:1", GatewayKey: "secret", DailyLimit: 1}
	s.state.Targets["a/solo"].State = "passed"
	s.state.Targets["a/solo"].CheckedAt = time.Now()
	s.mu.Unlock()
	if e := s.Trigger("a/solo", "milk", false); e != nil {
		t.Fatal(e)
	}
	state, _ := s.Snapshot()
	if len(state.Results) != 0 {
		t.Fatal("fresh target probed")
	}
	s.LogLine(`MAGPIE_QUALITY_TRIGGER {"model":"a/luna"}`)
	state, _ = s.Snapshot()
	if len(state.Targets) != 1 {
		t.Fatal("luna configured automatically")
	}
}

func TestUnavailableCooldown(t *testing.T) {
	s := fixture(t)
	s.SetTarget("a/solo", true)
	s.mu.Lock()
	s.state.Config = Config{GatewayURL: "http://127.0.0.1:1", GatewayKey: "fixture", DailyLimit: 120}
	target := s.state.Targets["a/solo"]
	target.State = "unavailable"
	target.RetryAfter = time.Now().Add(15 * time.Minute)
	s.mu.Unlock()
	if err := s.Trigger("a/solo", "milk", false); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Snapshot()
	if len(state.Results) != 0 || state.Targets["a/solo"].State != "unavailable" {
		t.Fatal("cooldown triggered paid probe")
	}
}
