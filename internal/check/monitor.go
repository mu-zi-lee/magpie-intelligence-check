package check

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const checkInterval = 15 * time.Minute

func (t *Target) needsCheck(now time.Time) bool {
	return t.Enabled && !t.Held && t.State != "checking" &&
		!t.AllowUntil.After(now) && !t.RetryAfter.After(now) &&
		(t.CheckedAt.IsZero() || now.Sub(t.CheckedAt) >= checkInterval)
}

// WatchTargets refreshes enabled targets even when no gateway log is available.
func (s *Service) WatchTargets() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.mu.Lock()
				var models []string
				if s.state.Config.GatewayURL != "" && s.state.Config.GatewayKey != "" {
					for model, target := range s.state.Targets {
						if target.needsCheck(time.Now()) {
							models = append(models, model)
						}
					}
				}
				s.mu.Unlock()
				for _, model := range models {
					// Trigger rechecks eligibility under the lock and owns the budget.
					_ = s.Trigger(model, "milk", false)
				}
			}
		}
	}()
}

func (s *Service) warnLocked(t *Target) {
	// Keep provider response bodies and credentials out of the host's log.
	warning, _ := json.Marshal(map[string]string{
		"model": t.Model, "state": t.State, "verdict": t.Verdict, "event": t.Event,
	})
	fmt.Fprintf(os.Stderr, "MAGPIE_QUALITY_WARNING %s\n", warning)
}
