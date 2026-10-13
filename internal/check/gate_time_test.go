package check

import (
	"fmt"
	"os/exec"
	"testing"
	"time"
)

func TestGateTimeBoundaries(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("Bun unavailable")
	}
	for _, tc := range []struct {
		name, state, verdict, mode string
		checked, allow             time.Time
		held, blocked              bool
	}{
		{name: "fresh", state: "passed", verdict: "passed", checked: time.Now()},
		{name: "expired", state: "passed", verdict: "passed", checked: time.Now().Add(-16 * time.Minute)},
		{name: "unverified", state: "unverified"},
		{name: "unavailable", state: "unavailable", verdict: "passed"},
		{name: "review", state: "review", verdict: "review"},
		{name: "checking", state: "checking", verdict: "review"},
		{name: "strict expired", state: "passed", verdict: "passed", mode: "strict", checked: time.Now().Add(-16 * time.Minute)},
		{name: "strict unverified", state: "unverified", mode: "strict"},
		{name: "strict unavailable", state: "unavailable", mode: "strict"},
		{name: "strict review", state: "review", verdict: "review", mode: "strict", blocked: true},
		{name: "strict rechecking", state: "checking", verdict: "review", mode: "strict", blocked: true},
		{name: "strict failed recheck", state: "unavailable", verdict: "review", mode: "strict", blocked: true},
		{name: "strict lease", state: "review", verdict: "review", mode: "strict", allow: time.Now().Add(time.Minute)},
		{name: "strict expired lease", state: "review", verdict: "review", mode: "strict", allow: time.Now().Add(-time.Second), blocked: true},
		{name: "held passed", state: "passed", verdict: "passed", checked: time.Now(), held: true, blocked: true},
		{name: "held lease", state: "passed", verdict: "passed", held: true, allow: time.Now().Add(time.Minute)},
		{name: "held expired lease", state: "passed", verdict: "passed", held: true, allow: time.Now().Add(-time.Second), blocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture(t)
			s.SetTarget("a/solo", true)
			s.mu.Lock()
			target := s.state.Targets["a/solo"]
			target.State = tc.state
			target.Verdict = tc.verdict
			target.Held = tc.held
			if tc.mode != "" {
				target.Mode = tc.mode
			}
			target.CheckedAt = tc.checked
			target.AllowUntil = tc.allow
			if err := s.saveLocked(); err != nil {
				t.Fatal(err)
			}
			s.mu.Unlock()
			assertGate(t, s, tc.blocked)
		})
	}
}

func assertGate(t *testing.T, s *Service, blocked bool) {
	t.Helper()
	script := fmt.Sprintf(`const m=await import(process.argv[1]);
let blocked=false;
const body={model:"a/solo",messages:[{role:"user",content:"business"}],stream:true};
const result=m.onRequest(body,{reject:s=>{if(s!==403){throw Error("wrong status");}blocked=true;}});
if(blocked!==%t){throw Error("wrong gate decision");}
if(!blocked&&result!==body){throw Error("business request was changed");}`, blocked)
	if out, err := exec.Command("bun", "-e", script, s.gate).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
}
