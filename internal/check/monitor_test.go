package check

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func waitTarget(t *testing.T, s *Service, state string) *Target {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, _ := s.Snapshot()
		target := snapshot.Targets["a/solo"]
		if target.State == state {
			return target
		}
		if time.Now().After(deadline) {
			t.Fatalf("target remained %s, want %s", target.State, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBackgroundCheckWithoutLog(t *testing.T) {
	for _, logPath := range []string{"", filepath.Join(t.TempDir(), "missing.log")} {
		t.Run(logPath, func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Write([]byte(`{"choices":[{"message":{"content":"46.24 inches"},"finish_reason":"stop"}]}`))
			}))
			defer up.Close()
			s := fixture(t)
			if err := s.Configure(Config{GatewayURL: up.URL, GatewayKey: "fixture", LogPath: logPath, DailyLimit: 1}); err != nil {
				t.Fatal(err)
			}
			s.SetTarget("a/solo", true)
			s.WatchTargets()
			passed := waitTarget(t, s, "passed")
			if passed.Verdict != "passed" || calls.Load() != 1 {
				t.Fatal("background check did not reach and grade the upstream")
			}
			s.mu.Lock()
			s.state.Targets["a/solo"].CheckedAt = time.Now().Add(-16 * time.Minute)
			s.mu.Unlock()
			limited := waitTarget(t, s, "unavailable")
			if limited.Reason != "每日请求预算已耗尽" || limited.Verdict != "passed" || !limited.RetryAfter.After(time.Now()) || calls.Load() != 1 {
				t.Fatal("budget consumed extra calls or erased the last quality verdict")
			}
			if _, err := exec.LookPath("bun"); err == nil {
				assertGate(t, s, false)
			}
		})
	}
}

func TestUnavailableCheckPreservesQualityVerdict(t *testing.T) {
	for _, answer := range []string{"46.24 inches", "48 inches"} {
		t.Run(answer, func(t *testing.T) {
			var unavailable atomic.Bool
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if unavailable.Load() {
					http.Error(w, "upstream unavailable", 503)
					return
				}
				fmt.Fprintf(w, `{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, answer)
			}))
			defer up.Close()
			s := fixture(t)
			s.Configure(Config{GatewayURL: up.URL, GatewayKey: "fixture", DailyLimit: 20})
			s.SetTarget("a/solo", true)
			if err := s.Trigger("a/solo", "milk", true); err != nil {
				t.Fatal(err)
			}
			waitIdle(t, s)
			before, _ := s.Snapshot()
			want := "passed"
			if answer == "48 inches" {
				want = "review"
			}
			if before.Targets["a/solo"].Verdict != want {
				t.Fatal("probe was not graded")
			}
			unavailable.Store(true)
			if err := s.Trigger("a/solo", "milk", true); err != nil {
				t.Fatal(err)
			}
			waitIdle(t, s)
			after, _ := s.Snapshot()
			target := after.Targets["a/solo"]
			if target.State != "unavailable" || target.Verdict != want || !target.CheckedAt.Equal(before.Targets["a/solo"].CheckedAt) {
				t.Fatal("failed recheck overwrote the quality conclusion")
			}
			if _, err := exec.LookPath("bun"); err == nil {
				assertGate(t, s, false)
				s.Action("a/solo", "strict")
				assertGate(t, s, want == "review")
			}
		})
	}
}

func TestHoldSurvivesSuccessfulCheck(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Write([]byte(`{"choices":[{"message":{"content":"46.24 inches"},"finish_reason":"stop"}]}`))
	}))
	defer up.Close()
	s := fixture(t)
	s.Configure(Config{GatewayURL: up.URL, GatewayKey: "fixture", DailyLimit: 20})
	s.SetTarget("a/solo", true)
	if err := s.Trigger("a/solo", "milk", true); err != nil {
		t.Fatal(err)
	}
	<-started
	s.Action("a/solo", "hold")
	close(release)
	waitIdle(t, s)
	state, _ := s.Snapshot()
	if !state.Targets["a/solo"].Held || state.Targets["a/solo"].Verdict != "passed" {
		t.Fatal("quality check changed the manual hold")
	}
	if _, err := exec.LookPath("bun"); err == nil {
		assertGate(t, s, true)
		s.Action("a/solo", "resume")
		assertGate(t, s, false)
	}
}

func TestRoutingGroupCannotEnableStrictMode(t *testing.T) {
	s := fixture(t)
	s.SetTarget("group/auto-model", true)
	if err := s.Action("group/auto-model", "strict"); err == nil {
		t.Fatal("strict gate accepted a routing group")
	}
	state, _ := s.Snapshot()
	if state.Targets["group/auto-model"].Mode != "monitor" {
		t.Fatal("rejected action changed mode")
	}
}

func TestLegacyTargetsMigrateToMonitoringAndKeepHolds(t *testing.T) {
	dir := t.TempDir()
	gate := filepath.Join(dir, "gate.js")
	store, err := OpenStore(filepath.Join(dir, "check.db"))
	if err != nil {
		t.Fatal(err)
	}
	old := State{Targets: map[string]*Target{
		"a/solo": {Model: "a/solo", Enabled: true, State: "passed", CheckedAt: time.Now().Add(-time.Hour)},
		"a/held": {Model: "a/held", Enabled: true, State: "review", Reason: "人工暂停"},
	}, Alias: Secret()}
	if err := store.Save(old); err != nil {
		t.Fatal(err)
	}
	store.Close()
	s, err := New(dir, gate)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state, _ := s.Snapshot()
	if state.Targets["a/solo"].Mode != "monitor" || state.Targets["a/solo"].Verdict != "passed" || !state.Targets["a/held"].Held {
		t.Fatal("legacy migration lost verdict or operator hold")
	}
	if _, err := exec.LookPath("bun"); err == nil {
		assertGate(t, s, false)
	}
}
