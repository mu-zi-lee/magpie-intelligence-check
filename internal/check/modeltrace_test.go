package check

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelTraceBatchBudgetAndGateIsolation(t *testing.T) {
	s := fixture(t)
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Model    string
			Messages []struct{ Content string }
		}
		json.NewDecoder(r.Body).Decode(&body)
		if !strings.HasPrefix(body.Model, "quality-probe/") || len(body.Messages) != 1 || !strings.Contains(body.Messages[0].Content, "1 到 355") {
			t.Error("independent probe payload invalid")
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"1,2,3"},"finish_reason":"stop"}],"usage":{"total_tokens":12}}`)
	}))
	defer up.Close()
	s.Configure(Config{GatewayURL: up.URL, GatewayKey: "fixture", DailyLimit: 3})
	s.SetTarget("a/solo", true)
	s.Action("a/solo", "hold")
	if err := s.TriggerModelTrace("a/solo"); err != nil {
		t.Fatal(err)
	}
	if err := s.TriggerModelTrace("a/solo"); err == nil {
		t.Fatal("concurrent probe accepted")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, _ := s.Snapshot()
		if len(state.Results) == 3 && state.Results[2].Verdict != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("batch not completed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, _ := s.Snapshot()
	if calls.Load() != 3 || !state.Targets["a/solo"].Held {
		t.Fatal("changed gate or wrong call count")
	}
	seen := map[int]bool{}
	for _, result := range state.Results {
		if result.Batch != state.Results[0].Batch || result.Tokens != 12 || result.PromptText == "" || seen[result.ExpectedCount] {
			t.Fatal("batch metadata missing or challenge counts reused")
		}
		if result.ExpectedCount < 292 || result.ExpectedCount > 332 {
			t.Fatal("invalid challenge length")
		}
		seen[result.ExpectedCount] = true
	}
	if err := s.TriggerModelTrace("a/solo"); err == nil {
		t.Fatal("budget exceeded")
	}
	if calls.Load() != 3 {
		t.Fatal("over-budget request sent")
	}
}

func TestModelTraceAssetsRequireAuthentication(t *testing.T) {
	s := fixture(t)
	h := s.Handler("secret")
	for _, path := range []string{"/modeltrace/panel.js", "/modeltrace/fingerprint-core.js", "/modeltrace/unified_bank.json"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatal("unauthenticated asset exposed", path)
		}
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer secret")
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatal("asset missing", path)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("X-Magpie-Quality") != "ready" {
		t.Fatal("invalid health probe")
	}
}
