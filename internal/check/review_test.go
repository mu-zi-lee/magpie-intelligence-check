package check

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCancelQualityRequest(t *testing.T) {
	s := fixture(t)
	started := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer up.Close()
	s.Configure(Config{GatewayURL: up.URL, GatewayKey: "fixture", DailyLimit: 20})
	s.SetTarget("a/solo", true)
	if err := s.Trigger("a/solo", "milk", true); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := s.CancelTest("a/solo"); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	state, _ := s.Snapshot()
	if len(state.Results) != 1 || state.Results[0].Error != "检测已取消" || state.Targets["a/solo"].State == "passed" {
		t.Fatal(state)
	}
	if s.CancelTest("a/solo") == nil {
		t.Fatal("completed test remained cancellable")
	}
}

func TestClearHistoryPreservesBudgetAndGate(t *testing.T) {
	s := fixture(t)
	s.Configure(Config{GatewayURL: "http://127.0.0.1:1", GatewayKey: "fixture", DailyLimit: 1})
	s.SetTarget("a/solo", true)
	s.Action("a/solo", "hold")
	s.state.Results = append(s.state.Results, Result{ID: "old", Model: "a/solo", Time: time.Now(), Answer: "private"})
	s.running["a/solo"] = true
	if s.ClearResults([]string{"old"}) == nil {
		t.Fatal("cleared while running")
	}
	delete(s.running, "a/solo")
	if err := s.ClearResults([]string{"old"}); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Snapshot()
	if len(state.Results) != 0 || !state.Targets["a/solo"].Held {
		t.Fatal(state)
	}
	if err := s.Trigger("a/solo", "repeat", true); err == nil || !strings.Contains(err.Error(), "预算") {
		t.Fatal("clearing restored budget", err)
	}
	var stored State
	if err := s.store.Load(&stored); err != nil || stored.ClearedUsage[time.Now().UTC().Format("2006-01-02")]["a/solo"] != 1 {
		t.Fatal("budget not persisted", err)
	}
}

func TestDisabledTargetAllowsManualTestsOnly(t *testing.T) {
	s := fixture(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"manual answer"},"finish_reason":"stop"}]}`))
	}))
	defer up.Close()
	s.Configure(Config{GatewayURL: up.URL, GatewayKey: "fixture", DailyLimit: 20})
	s.SetTarget("a/solo", false)
	if err := s.Trigger("a/solo", "repeat", false); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Snapshot()
	if len(state.Results) != 0 {
		t.Fatal("disabled target auto-tested")
	}
	if err := s.Trigger("a/solo", "repeat", true); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	if err := s.TriggerModelTrace("a/solo"); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	state, _ = s.Snapshot()
	if len(state.Results) != 4 || state.Targets["a/solo"].Enabled {
		t.Fatal("manual test changed monitoring")
	}
}

func TestReviewPersistsWithoutChangingVerdictOrGate(t *testing.T) {
	s := fixture(t)
	s.SetTarget("a/solo", true)
	s.Action("a/solo", "hold")
	s.state.Results = append(s.state.Results, Result{ID: "answer", Model: "a/solo", Prompt: "repeat", Answer: "original", Verdict: "manual", Time: time.Now()})
	if err := s.ReviewResult("answer", "accepted", "checked"); err != nil {
		t.Fatal(err)
	}
	var stored State
	if err := s.store.Load(&stored); err != nil {
		t.Fatal(err)
	}
	r := stored.Results[0]
	if r.Answer != "original" || r.Verdict != "manual" || r.Review != "accepted" || r.ReviewNote != "checked" || r.ReviewedAt.IsZero() {
		t.Fatal(r)
	}
	if !stored.Targets["a/solo"].Held {
		t.Fatal("human review changed gate")
	}
	if s.ReviewResult("answer", "passed", "") == nil || s.ReviewResult("missing", "pending", "") == nil {
		t.Fatal("invalid review accepted")
	}
	s.state.Results = append(s.state.Results, Result{ID: "running", Verdict: "running"})
	if s.ReviewResult("running", "accepted", "") == nil {
		t.Fatal("reviewed pending answer")
	}
}

func TestDeleteTargetPreservesHistoryAndRemovesGate(t *testing.T) {
	s := fixture(t)
	s.SetTarget("a/solo", true)
	s.state.Results = append(s.state.Results, Result{ID: "old", Model: "a/solo", Answer: "kept"})
	s.running["a/solo"] = true
	if s.DeleteTarget("a/solo") == nil {
		t.Fatal("deleted active target")
	}
	delete(s.running, "a/solo")
	if err := s.DeleteTarget("a/solo"); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Snapshot()
	if len(state.Targets) != 0 || len(state.Results) != 1 {
		t.Fatal("history removed or target retained")
	}
	gate, _ := os.ReadFile(s.gate)
	if strings.Contains(string(gate), "a/solo") {
		t.Fatal("deleted target remains gated")
	}
}
