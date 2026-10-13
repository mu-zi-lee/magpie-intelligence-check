package check

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDetectionLifecycle(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Model    string
			Messages []struct{ Content string }
		}
		json.NewDecoder(r.Body).Decode(&body)
		if !strings.HasPrefix(body.Model, "quality-probe/") {
			t.Error("probe bypass missing")
		}
		milk, _ := FindPrompt("milk")
		if len(body.Messages) != 1 || body.Messages[0].Content != milk.Text {
			t.Error("probe prompt changed")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"46.24 inches"},"finish_reason":"stop"}]}`))
	}))
	defer up.Close()
	s := fixture(t)
	s.Configure(Config{GatewayURL: up.URL, GatewayKey: "fixture", DailyLimit: 120})
	s.SetTarget("a/solo", true)
	s.LogLine(`middleware quality-check [info]: MAGPIE_QUALITY_TRIGGER {"model":"a/solo"}`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, _ := s.Snapshot()
		if state.Targets["a/solo"].State == "passed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("probe did not pass")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.LogLine(`MAGPIE_QUALITY_TRIGGER {"model":"a/solo"}`)
	if calls.Load() != 1 {
		t.Fatal("fresh result triggered again")
	}
	if _, err := exec.LookPath("bun"); err == nil {
		script := `const m=await import(process.argv[1]); const reject=(status,message)=>{throw Error(status+":"+message)};m.onRequest({model:"a/solo"},{reject});m.onRequest({model:"a/luna"},{reject});`
		if out, err := exec.Command("bun", "-e", script, s.gate).CombinedOutput(); err != nil {
			t.Fatalf("passed gate: %s %v", out, err)
		}
		s.Action("a/solo", "hold")
		script = `const m=await import(process.argv[1]);let blocked=false;m.onRequest({model:"a/solo"},{reject:(s)=>{blocked=s===403}});if(!blocked)throw Error("not blocked");m.onRequest({model:"a/luna"},{reject:()=>{throw Error("luna blocked")}});`
		if out, err := exec.Command("bun", "-e", script, s.gate).CombinedOutput(); err != nil {
			t.Fatalf("hold gate: %s %v", out, err)
		}
	}
}

func waitIdle(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		running := len(s.running) > 0
		s.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("检测未结束")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFailureAndManualState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		answer string
		limit  int
		want   string
		calls  int
	}{
		{"confirmed", 200, "48 inches", 120, "review", 2},
		{"limited", 429, "", 120, "unavailable", 1},
		{"budget", 200, "48 inches", 1, "unavailable", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": tc.answer}, "finish_reason": "stop"}}})
			}))
			defer up.Close()
			s := fixture(t)
			s.Configure(Config{GatewayURL: up.URL, GatewayKey: "fixture", DailyLimit: tc.limit})
			s.SetTarget("a/solo", true)
			if err := s.Trigger("a/solo", "milk", true); err != nil {
				t.Fatal(err)
			}
			waitIdle(t, s)
			state, _ := s.Snapshot()
			if state.Targets["a/solo"].State != tc.want || int(calls.Load()) != tc.calls {
				t.Fatalf("state=%s calls=%d", state.Targets["a/solo"].State, calls.Load())
			}
		})
	}
	s := fixture(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"<svg/>"},"finish_reason":"stop"}]}`))
	}))
	defer up.Close()
	s.Configure(Config{GatewayURL: up.URL, GatewayKey: "fixture", DailyLimit: 120})
	s.SetTarget("a/solo", true)
	s.Action("a/solo", "hold")
	if err := s.Trigger("a/solo", "svg", true); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	state, _ := s.Snapshot()
	if !state.Targets["a/solo"].Held {
		t.Fatal("手动作品题覆盖门禁状态")
	}
}
