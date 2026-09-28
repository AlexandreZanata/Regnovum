package synth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/platform/synth"
)

// probeScript programs one scripted backend: statuses, bodies and a
// record of every request the probes made.
type probeScript struct {
	mu       sync.Mutex
	live     int
	ready    int
	login    int
	logout   int
	feed     int
	metrics  int
	webhook  int
	lag      string
	requests []string
}

func (script *probeScript) record(r *http.Request) {
	script.mu.Lock()
	defer script.mu.Unlock()
	script.requests = append(script.requests, r.Method+" "+r.URL.Path)
}

func (script *probeScript) hits(name string) int {
	script.mu.Lock()
	defer script.mu.Unlock()
	switch name {
	case "login":
		return script.login
	case "logout":
		return script.logout
	case "live":
		return script.live
	case "feed":
		return script.feed
	}
	return -1
}

func (script *probeScript) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		script.record(r)
		script.mu.Lock()
		code := script.live
		script.mu.Unlock()
		if code == 0 {
			code = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"status":"live"}`))
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		script.record(r)
		script.mu.Lock()
		code := script.ready
		script.mu.Unlock()
		if code == 0 {
			code = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if code == http.StatusOK {
			_, _ = w.Write([]byte(`{"status":"ready"}`))
		} else {
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
		}
	})
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		script.record(r)
		script.mu.Lock()
		code := script.login
		script.mu.Unlock()
		if code == 0 {
			code = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		if code == http.StatusOK {
			w.Header().Set("Set-Cookie", "arena_session=synth-session; Path=/; HttpOnly")
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST /api/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		script.record(r)
		script.mu.Lock()
		code := script.logout
		script.mu.Unlock()
		if code == 0 {
			code = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"status":"logged_out"}`))
	})
	mux.HandleFunc("GET /api/v1/arenas", func(w http.ResponseWriter, r *http.Request) {
		script.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		script.record(r)
		script.mu.Lock()
		lag := script.lag
		script.mu.Unlock()
		if lag == "" {
			lag = "12"
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("jobs_lag_seconds " + lag + "\n"))
	})
	mux.HandleFunc("POST /hooks/provider", func(w http.ResponseWriter, r *http.Request) {
		script.record(r)
		script.mu.Lock()
		code := script.webhook
		script.mu.Unlock()
		if code == 0 {
			code = http.StatusBadRequest
		}
		w.WriteHeader(code)
	})
	return mux
}

var probeInstant = time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)

func probeRunner() synth.Runner {
	return synth.Runner{
		Now:          func() time.Time { return probeInstant },
		Timeout:      5 * time.Second,
		MaxLag:       60 * time.Second,
		MaxBackupAge: 24 * time.Hour,
	}
}

func freshCredential() synth.SyntheticCredential {
	return synth.SyntheticCredential{
		ID:       "synth-1",
		Email:    "synthetic-probe@example.invalid",
		Secret:   "synth-secret-value-9",
		Scope:    "probe:login",
		IssuedAt: probeInstant.Add(-time.Minute),
		ValidFor: time.Hour,
	}
}

func runProbes(t *testing.T, script *probeScript, mutate func(*probeScript, *synth.Target, *synth.SyntheticCredential)) []synth.Result {
	t.Helper()
	server := httptest.NewServer(script.handler())
	defer server.Close()
	target := synth.Target{
		BaseURL:     server.URL,
		WebhookPath: "/hooks/provider",
		Backup: func(context.Context) (synth.BackupState, error) {
			return synth.BackupState{Age: time.Hour}, nil
		},
	}
	credential := freshCredential()
	if mutate != nil {
		mutate(script, &target, &credential)
	}
	return probeRunner().Run(context.Background(), target, credential)
}

func resultByName(results []synth.Result, name string) synth.Result {
	for _, result := range results {
		if result.Name == name {
			return result
		}
	}
	return synth.Result{Name: "missing:" + name, Failed: true}
}

// TestProbeRunAllGreen proves the quiet path: seven checks, no failure,
// no skip, against a healthy scripted backend.
func TestProbeRunAllGreen(t *testing.T) {
	t.Parallel()

	results := runProbes(t, &probeScript{}, nil)
	if len(results) != 7 {
		t.Fatalf("results = %d, want the seven checks", len(results))
	}
	for _, result := range results {
		if result.Failed || result.Skipped {
			t.Errorf("%s failed/skipped: %+v", result.Name, result)
		}
		if result.Alert != "" {
			t.Errorf("%s carries alert %q on the green path", result.Name, result.Alert)
		}
	}
}

// TestProbeFailuresRaiseTheirAlert breaks one axis at a time and proves
// each failure raises exactly its own stable alert.
func TestProbeFailuresRaiseTheirAlert(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		check string
		alert string
		set   func(*probeScript)
	}{
		{name: "live down", check: "live", alert: synth.AlertLiveDown, set: func(s *probeScript) { s.live = 500 }},
		{name: "unready", check: "ready", alert: synth.AlertUnready, set: func(s *probeScript) { s.ready = 503 }},
		{name: "login refused", check: "login", alert: synth.AlertLoginFailed, set: func(s *probeScript) { s.login = 401 }},
		{name: "logout fails cleanup", check: "login", alert: synth.AlertLoginCleanupFailed, set: func(s *probeScript) { s.logout = 500 }},
		{name: "lag over maximum", check: "worker-lag", alert: synth.AlertWorkerLag, set: func(s *probeScript) { s.lag = "61" }},
		{name: "forgery accepted", check: "webhook", alert: synth.AlertWebhookForgeryAccepted, set: func(s *probeScript) { s.webhook = 200 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			results := runProbes(t, &probeScript{}, func(script *probeScript, _ *synth.Target, _ *synth.SyntheticCredential) {
				test.set(script)
			})
			got := resultByName(results, test.check)
			if !got.Failed || got.Alert != test.alert {
				t.Fatalf("%s = %+v, want failure with %q", test.check, got, test.alert)
			}
			for _, result := range results {
				if result.Name != test.check && (result.Failed || result.Skipped) {
					t.Errorf("collateral %s = %+v", result.Name, result)
				}
			}
		})
	}

	t.Run("backup stale", func(t *testing.T) {
		t.Parallel()

		results := runProbes(t, &probeScript{}, func(_ *probeScript, target *synth.Target, _ *synth.SyntheticCredential) {
			target.Backup = func(context.Context) (synth.BackupState, error) {
				return synth.BackupState{Age: 25 * time.Hour}, nil
			}
		})
		got := resultByName(results, "restore")
		if !got.Failed || got.Alert != synth.AlertBackupStale {
			t.Fatalf("restore = %+v, want %q", got, synth.AlertBackupStale)
		}
	})

	t.Run("lag at the boundary passes", func(t *testing.T) {
		t.Parallel()

		results := runProbes(t, &probeScript{lag: "60"}, nil)
		if got := resultByName(results, "worker-lag"); got.Failed || got.Skipped {
			t.Fatalf("lag at maximum = %+v, want a pass", got)
		}
	})
}

// TestProbeRequestsStayOnTheAllowlist proves the safety contract: a full
// green run only speaks the allowlisted shapes, so probes cannot mint
// money, content or sessions outside login/logout.
func TestProbeRequestsStayOnTheAllowlist(t *testing.T) {
	t.Parallel()

	script := &probeScript{}
	runProbes(t, script, nil)

	allowed := map[string]bool{
		"GET /health/live":         true,
		"GET /health/ready":        true,
		"POST /api/v1/auth/login":  true,
		"POST /api/v1/auth/logout": true,
		"GET /api/v1/arenas":       true,
		"GET /metrics":             true,
		"POST /hooks/provider":     true,
	}
	script.mu.Lock()
	defer script.mu.Unlock()
	if len(script.requests) == 0 {
		t.Fatal("the green run made no requests")
	}
	for _, request := range script.requests {
		if !allowed[request] {
			t.Errorf("probe left the allowlist: %q", request)
		}
	}
}

// TestProbeNeverKeepsSecrets proves cleanup and secrecy: a refused login
// never reaches logout, and the credential secret appears in no result,
// green or red.
func TestProbeNeverKeepsSecrets(t *testing.T) {
	t.Parallel()

	script := &probeScript{login: 401}
	results := runProbes(t, script, nil)
	if got := resultByName(results, "login"); !got.Failed || got.Alert != synth.AlertLoginFailed {
		t.Fatalf("login = %+v, want the refusal", got)
	}
	if hits := script.hits("logout"); hits != 0 {
		t.Fatalf("logout hits = %d, want none: nothing was opened", hits)
	}

	encoded, err := json.Marshal(results)
	if err != nil {
		t.Fatalf("results do not marshal: %v", err)
	}
	if strings.Contains(string(encoded), "synth-secret-value-9") {
		t.Fatalf("results leak the credential secret: %s", encoded)
	}
}

// TestStaleCredentialSkipsLoginOnly proves rotation is enforced: a stale
// credential skips the authenticated check with the rotation alert while
// the read-only checks still run.
func TestStaleCredentialSkipsLoginOnly(t *testing.T) {
	t.Parallel()

	script := &probeScript{}
	stale := freshCredential()
	stale.IssuedAt = probeInstant.Add(-2 * time.Hour)
	server := httptest.NewServer(script.handler())
	defer server.Close()
	results := probeRunner().Run(context.Background(), synth.Target{
		BaseURL:     server.URL,
		WebhookPath: "/hooks/provider",
		Backup: func(context.Context) (synth.BackupState, error) {
			return synth.BackupState{Age: time.Hour}, nil
		},
	}, stale)

	got := resultByName(results, "login")
	if got.Failed || !got.Skipped || got.Alert != synth.AlertCredentialStale {
		t.Fatalf("login = %+v, want skipped with %q", got, synth.AlertCredentialStale)
	}
	if hits := script.hits("login"); hits != 0 {
		t.Fatalf("login hits = %d, want none with a stale credential", hits)
	}
	for _, name := range []string{"live", "public-feed", "worker-lag", "webhook", "restore"} {
		if result := resultByName(results, name); result.Failed || result.Skipped {
			t.Errorf("%s = %+v, want a pass while login is skipped", name, result)
		}
	}
}

// TestUnconfiguredProbesReportSkipped proves explicit degradation: with
// no webhook path and no backup inventory, those checks report skipped
// instead of silently passing.
func TestUnconfiguredProbesReportSkipped(t *testing.T) {
	t.Parallel()

	script := &probeScript{}
	server := httptest.NewServer(script.handler())
	defer server.Close()
	results := probeRunner().Run(context.Background(), synth.Target{BaseURL: server.URL}, freshCredential())

	for _, name := range []string{"webhook", "restore"} {
		if got := resultByName(results, name); got.Failed || !got.Skipped || got.Alert != synth.AlertProbeUnconfigured {
			t.Errorf("%s = %+v, want skipped unconfigured", name, got)
		}
	}
	for _, name := range []string{"live", "ready", "login", "public-feed", "worker-lag"} {
		if got := resultByName(results, name); got.Failed || got.Skipped {
			t.Errorf("%s = %+v, want a pass", name, got)
		}
	}
}

// TestCredentialExpiry proves rotation arithmetic: fresh passes, stale
// fails, and a zero lifetime is always stale.
func TestCredentialExpiry(t *testing.T) {
	t.Parallel()

	if freshCredential().Expired(probeInstant) {
		t.Error("a fresh credential must not be expired")
	}
	stale := freshCredential()
	stale.IssuedAt = probeInstant.Add(-2 * time.Hour)
	if !stale.Expired(probeInstant) {
		t.Error("a two-hour-old hourly credential must be expired")
	}
	zero := freshCredential()
	zero.ValidFor = 0
	if !zero.Expired(probeInstant) {
		t.Error("a zero-lifetime credential must always be expired")
	}
}
