// Package synth owns the safe synthetic backend probes of P29-T06: small
// HTTP checks that exercise the paths a monitor watches — liveness,
// readiness, synthetic login, the public journey, worker lag, a forged
// provider webhook and backup freshness — without real data, without
// financial effect and without persistent public content.
//
// Safety is structural, not promised: probes only speak the allowlisted
// request shapes below (GETs plus login/logout plus one forged webhook
// that must be refused), the synthetic credential carries a minimal scope
// and an expiry the runner enforces before any authenticated check, and
// the login check revokes the session it opened. The webhook path and
// the backup inventory are composed by the operator because neither is a
// fixed product surface: an empty path or a nil inventory reports the
// check as explicitly unconfigured instead of silently passing it.
package synth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Alert names are the stable vocabulary the monitors key on. One check,
// one alert: a firing probe names exactly what broke.
const (
	AlertLiveDown               = "probe-live-down"
	AlertUnready                = "probe-unready"
	AlertLoginFailed            = "probe-login-failed"
	AlertLoginCleanupFailed     = "probe-login-cleanup-failed"
	AlertPublicJourneyFailed    = "probe-public-journey-failed"
	AlertWorkerLag              = "probe-worker-lag"
	AlertWebhookForgeryAccepted = "probe-webhook-forgery-accepted"
	AlertBackupStale            = "probe-backup-stale"
	AlertCredentialStale        = "probe-credential-stale"
	AlertProbeUnconfigured      = "probe-unconfigured"
)

// SyntheticCredential is the login the probes authenticate with. Scope is
// a documentation tag the composition root issues (for example
// "probe:login"); the credential opens sessions and nothing else, and
// ValidFor bounds its life so rotation is enforced, not remembered.
type SyntheticCredential struct {
	ID       string
	Email    string
	Secret   string
	Scope    string
	IssuedAt time.Time
	ValidFor time.Duration
}

// Expired reports whether the credential is past rotation at now. The
// instant travels as a parameter so the check stays pure: callers pass
// their clock, tests pass a fixed one.
func (credential SyntheticCredential) Expired(now time.Time) bool {
	if credential.ValidFor <= 0 {
		return true
	}
	return !now.Before(credential.IssuedAt.Add(credential.ValidFor))
}

// BackupState is what the operator's inventory reports: how old the
// newest verified backup is.
type BackupState struct {
	Age time.Duration
}

// Target is everything a probe run needs: the base URL, the client, the
// operator-composed webhook path (empty means the forgery check is
// explicitly unconfigured) and the backup inventory (nil means the
// restoration check is explicitly unconfigured).
type Target struct {
	BaseURL     string
	Client      *http.Client
	WebhookPath string
	Backup      func(ctx context.Context) (BackupState, error)
}

// Result is the verdict of one check. Skipped marks an explicitly
// unconfigured check; it is neither green nor a failure.
type Result struct {
	Name    string
	Failed  bool
	Skipped bool
	Alert   string
	Detail  string
}

// Runner executes the fixed check list with one timeout per check. Now
// and MaxLag/MaxBackupAge travel with the runner so tests pin them.
type Runner struct {
	Now          func() time.Time
	Timeout      time.Duration
	MaxLag       time.Duration
	MaxBackupAge time.Duration
}

// Run executes every check in order and returns one result per check.
// A stale credential skips the login check with the rotation alert;
// read-only checks always run.
func (runner Runner) Run(ctx context.Context, target Target, credential SyntheticCredential) []Result {
	if runner.Now == nil || runner.Timeout <= 0 {
		return []Result{{Name: "runner", Failed: true, Alert: AlertProbeUnconfigured, Detail: "runner needs Now and a positive Timeout"}}
	}
	client := target.Client
	if client == nil {
		client = http.DefaultClient
	}

	results := make([]Result, 0, 7)
	fail := func(name, alert, format string, args ...any) {
		results = append(results, Result{Name: name, Failed: true, Alert: alert, Detail: fmt.Sprintf(format, args...)})
	}
	pass := func(name string) {
		results = append(results, Result{Name: name})
	}

	if checkErr := checkStatus(ctx, client, target.BaseURL+"/health/live", runner.Timeout, "live"); checkErr != nil {
		fail("live", AlertLiveDown, "%v", checkErr)
	} else {
		pass("live")
	}
	if checkErr := checkStatus(ctx, client, target.BaseURL+"/health/ready", runner.Timeout, "ready"); checkErr != nil {
		fail("ready", AlertUnready, "%v", checkErr)
	} else {
		pass("ready")
	}

	if credential.Expired(runner.Now()) {
		results = append(results, Result{Name: "login", Skipped: true, Alert: AlertCredentialStale, Detail: "synthetic credential past rotation; rotate before probing authenticated paths"})
	} else if checkErr := checkLogin(ctx, client, target.BaseURL, runner.Timeout, credential); checkErr != nil {
		fail("login", checkErr.alert, "%v", checkErr.err)
	} else {
		pass("login")
	}

	if checkErr := checkFeed(ctx, client, target.BaseURL, runner.Timeout); checkErr != nil {
		fail("public-feed", AlertPublicJourneyFailed, "%v", checkErr)
	} else {
		pass("public-feed")
	}
	if checkErr := checkLag(ctx, client, target.BaseURL, runner.Timeout, runner.MaxLag); checkErr != nil {
		fail("worker-lag", AlertWorkerLag, "%v", checkErr)
	} else {
		pass("worker-lag")
	}

	if target.WebhookPath == "" {
		results = append(results, Result{Name: "webhook", Skipped: true, Alert: AlertProbeUnconfigured, Detail: "no webhook path composed; wire the provider callback path to enable the forgery check"})
	} else if checkErr := checkWebhook(ctx, client, target.BaseURL+target.WebhookPath, runner.Timeout); checkErr != nil {
		fail("webhook", checkErr.alert, "%v", checkErr.err)
	} else {
		pass("webhook")
	}

	if target.Backup == nil {
		results = append(results, Result{Name: "restore", Skipped: true, Alert: AlertProbeUnconfigured, Detail: "no backup inventory composed; wire the backup listing to enable the restoration check"})
	} else if checkErr := checkBackup(ctx, target.Backup, runner.MaxBackupAge); checkErr != nil {
		fail("restore", AlertBackupStale, "%v", checkErr)
	} else {
		pass("restore")
	}

	return results
}

// checkStatus asserts a health document: 200 with {"status": want}.
func checkStatus(ctx context.Context, client *http.Client, url string, timeout time.Duration, want string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return fmt.Errorf("health document is not JSON: %w", err)
	}
	if response.StatusCode != http.StatusOK || body.Status != want {
		return fmt.Errorf("status %d with %q, want 200 with %q", response.StatusCode, body.Status, want)
	}
	return nil
}

// loginFailure carries the alert a login attempt deserves: a refused
// login and a leaked session are different pages.
type loginFailure struct {
	alert string
	err   error
}

// checkLogin opens a synthetic session and revokes it before returning:
// a probe that cannot clean up after itself must not run. The secret
// never enters any result; failures carry shapes, not values.
func checkLogin(ctx context.Context, client *http.Client, base string, timeout time.Duration, credential SyntheticCredential) *loginFailure {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload, err := json.Marshal(map[string]string{"email": credential.Email, "password": credential.Secret})
	if err != nil {
		return &loginFailure{alert: AlertLoginFailed, err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/auth/login", bytes.NewReader(payload))
	if err != nil {
		return &loginFailure{alert: AlertLoginFailed, err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return &loginFailure{alert: AlertLoginFailed, err: err}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		return &loginFailure{alert: AlertLoginFailed, err: fmt.Errorf("login answered %d", response.StatusCode)}
	}

	logout, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/auth/logout", nil)
	if err != nil {
		return &loginFailure{alert: AlertLoginCleanupFailed, err: err}
	}
	for _, cookie := range response.Cookies() {
		logout.AddCookie(cookie)
	}
	revoked, err := client.Do(logout)
	if err != nil {
		return &loginFailure{alert: AlertLoginCleanupFailed, err: err}
	}
	defer revoked.Body.Close()
	_, _ = io.Copy(io.Discard, revoked.Body)
	if revoked.StatusCode < 200 || revoked.StatusCode >= 300 {
		return &loginFailure{alert: AlertLoginCleanupFailed, err: fmt.Errorf("logout answered %d: synthetic session may be open", revoked.StatusCode)}
	}
	return nil
}

// checkFeed asserts the public journey answers machine-readable JSON.
// It is a GET: the probe observes the public surface, never writes it.
func checkFeed(ctx context.Context, client *http.Client, base string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/arenas", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var document any
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		return fmt.Errorf("public feed is not JSON: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("public feed answered %d", response.StatusCode)
	}
	return nil
}

// checkLag scrapes the exposition for jobs_lag_seconds and compares it
// with the maximum: the alert watches the wait of the oldest due job.
func checkLag(ctx context.Context, client *http.Client, base string, timeout time.Duration, max time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/metrics", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "jobs_lag_seconds" {
			continue
		}
		seconds, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return fmt.Errorf("jobs_lag_seconds %q does not parse", fields[1])
		}
		if time.Duration(seconds*float64(time.Second)) > max {
			return fmt.Errorf("worker lag %vs exceeds %v", fields[1], max)
		}
		return nil
	}
	return fmt.Errorf("jobs_lag_seconds absent from the exposition")
}

// forgedPayload is the inert forgery the webhook check presents: an
// unknown session of a synthetic origin. A pipeline that accepts it
// credits nothing real, but the acceptance itself is the emergency.
var forgedPayload = []byte(`{"id":"synth-forgery","type":"checkout.session.completed","session":"cs_synth_unknown"}`)

// webhookFailure separates a refused forgery (the healthy path, nil)
// from an accepted one (the emergency) and from transport errors.
type webhookFailure struct {
	alert string
	err   error
}

// checkWebhook presents the forgery and demands refusal: any 2xx means
// the pipeline would honor an unsigned event.
func checkWebhook(ctx context.Context, client *http.Client, url string, timeout time.Duration) *webhookFailure {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(forgedPayload))
	if err != nil {
		return &webhookFailure{alert: AlertPublicJourneyFailed, err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return &webhookFailure{alert: AlertPublicJourneyFailed, err: err}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return &webhookFailure{alert: AlertWebhookForgeryAccepted, err: fmt.Errorf("forged event answered %d: unsigned events are honored", response.StatusCode)}
	}
	return nil
}

// checkBackup judges the newest verified backup by age: a restoration
// path without a fresh artifact is the alert.
func checkBackup(ctx context.Context, inventory func(ctx context.Context) (BackupState, error), max time.Duration) error {
	state, err := inventory(ctx)
	if err != nil {
		return err
	}
	if state.Age > max {
		return fmt.Errorf("newest verified backup is %v old, older than %v", state.Age, max)
	}
	return nil
}
