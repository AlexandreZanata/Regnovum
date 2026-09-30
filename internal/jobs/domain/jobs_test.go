package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
)

func validJob(t *testing.T) domain.Job {
	t.Helper()
	created := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	return domain.Job{
		ID:          "0199c9f4-0000-7000-8000-000000000001",
		Type:        domain.TypeEmailDelivery,
		Version:     1,
		Payload:     []byte(`{"account_id":"a1","template":"verify"}`),
		State:       domain.StateQueued,
		AvailableAt: created,
		Attempts:    0,
		MaxAttempts: domain.DefaultMaxAttempts,
		CreatedAt:   created,
		UpdatedAt:   created,
	}
}

func TestJobVocabularyIsClosed(t *testing.T) {
	wantStates := []domain.JobState{"queued", "leased", "succeeded", "dead"}
	if len(domain.AllJobStates) != len(wantStates) {
		t.Fatalf("states: got %d, want %d", len(domain.AllJobStates), len(wantStates))
	}
	for index, want := range wantStates {
		if domain.AllJobStates[index] != want {
			t.Errorf("state[%d] = %q, want %q", index, domain.AllJobStates[index], want)
		}
		if !want.IsValid() {
			t.Errorf("%q should be valid", want)
		}
		if "unknown" == string(want) {
			t.Error("unreachable")
		}
		if domain.JobState("pending").IsValid() {
			t.Error("unknown state must be rejected")
		}
	}

	for _, state := range []domain.JobState{domain.StateSucceeded, domain.StateDead} {
		if !state.IsTerminal() {
			t.Errorf("%q should be terminal", state)
		}
	}
	for _, state := range []domain.JobState{domain.StateQueued, domain.StateLeased} {
		if state.IsTerminal() {
			t.Errorf("%q should not be terminal", state)
		}
	}
}

func TestJobTypeVocabularyIsClosed(t *testing.T) {
	want := []domain.JobType{
		"email_delivery", "ink_grant_monthly", "pass_expiry",
		"retention_run", "session_cleanup", "billing_reconciliation",
	}
	if len(domain.AllJobTypes) != len(want) {
		t.Fatalf("types: got %d, want %d", len(domain.AllJobTypes), len(want))
	}
	for index, expected := range want {
		if domain.AllJobTypes[index] != expected {
			t.Errorf("type[%d] = %q, want %q", index, domain.AllJobTypes[index], expected)
		}
		if !expected.IsValid() {
			t.Errorf("%q should be valid", expected)
		}
	}
	for _, unknown := range []domain.JobType{"", "send_email", "EMAIL_DELIVERY", "email delivery"} {
		if unknown.IsValid() {
			t.Errorf("%q must be rejected", unknown)
		}
	}
}

func TestValidatePayloadAcceptsBoundedObjects(t *testing.T) {
	accepted := []string{
		`{}`,
		`{"account_id":"a1"}`,
		`{"ids":["a","b"],"reference":"ink-1"}`,
		`{"nested":{"k":"v"}}`,
		`{"text":"quote \" and backslash \\ inside"}`,
	}
	for _, payload := range accepted {
		if err := domain.ValidatePayload([]byte(payload)); err != nil {
			t.Errorf("payload %s should be accepted: %v", payload, err)
		}
	}
}

func TestValidatePayloadRejectsInvalidDocuments(t *testing.T) {
	if err := domain.ValidatePayload(nil); !errors.Is(err, domain.ErrEmptyPayload) {
		t.Errorf("empty payload: got %v", err)
	}
	if err := domain.ValidatePayload([]byte("  ")); !errors.Is(err, domain.ErrPayloadNotObject) {
		t.Errorf("blank payload: got %v", err)
	}
	oversized := `{"pad":"` + strings.Repeat("x", domain.MaxPayloadBytes) + `"}`
	if err := domain.ValidatePayload([]byte(oversized)); !errors.Is(err, domain.ErrPayloadTooLarge) {
		t.Errorf("oversized payload: got %v", err)
	}
	for _, notObject := range []string{`[]`, `"text"`, `42`, `null`, `{"a":1}x`} {
		if err := domain.ValidatePayload([]byte(notObject)); !errors.Is(err, domain.ErrPayloadNotObject) {
			t.Errorf("payload %s should be refused as non-object: %v", notObject, err)
		}
	}
	// Structural check: these are unbalanced or unterminated. A document that
	// is balanced but not valid JSON (for example `{"a":}`) passes here and
	// is refused by the jsonb column, which is the parsing authority.
	for _, malformed := range []string{`{`, `{"a":"unterminated}`, `{"a":1}}`} {
		if err := domain.ValidatePayload([]byte(malformed)); err == nil {
			t.Errorf("payload %s should be malformed", malformed)
		}
	}
	tooDeep := `{"a":{"b":{"c":{"d":1}}}}`
	if err := domain.ValidatePayload([]byte(tooDeep)); !errors.Is(err, domain.ErrPayloadTooDeep) {
		t.Errorf("deep payload: got %v", err)
	}
	control := "{\"a\":\"line\nbreak\"}"
	if err := domain.ValidatePayload([]byte(control)); !errors.Is(err, domain.ErrPayloadMalformed) {
		t.Errorf("control character: got %v", err)
	}
	if err := domain.ValidatePayload([]byte{0xff, 0xfe, '{', '}'}); !errors.Is(err, domain.ErrPayloadMalformed) {
		t.Errorf("invalid utf-8: got %v", err)
	}
}

func TestFailureCodeVocabularyIsClosed(t *testing.T) {
	want := []domain.FailureCode{
		"JOB_INVALID_PAYLOAD", "JOB_UNKNOWN_TYPE", "JOB_UNSUPPORTED_VERSION",
		"JOB_HANDLER_ERROR", "JOB_HANDLER_TIMEOUT", "JOB_LEASE_EXPIRED",
	}
	if len(domain.AllFailureCodes) != len(want) {
		t.Fatalf("codes: got %d, want %d", len(domain.AllFailureCodes), len(want))
	}
	for index, expected := range want {
		if domain.AllFailureCodes[index] != expected {
			t.Errorf("code[%d] = %q, want %q", index, domain.AllFailureCodes[index], expected)
		}
		if !expected.IsValid() {
			t.Errorf("%q should be valid", expected)
		}
	}
	if domain.FailureCode("JOB_SOMETHING").IsValid() {
		t.Error("unknown failure code must be rejected")
	}
	if _, err := domain.NewFailure("JOB_NOPE", "x"); !errors.Is(err, domain.ErrUnknownFailureCode) {
		t.Errorf("unknown code: got %v", err)
	}
}

func TestFailureDetailIsRedacted(t *testing.T) {
	cases := map[string]string{
		"postgres://arena:secret@db:5432/arena": "postgres:arena:secretdb:5432arena",
		"user@example.com failed":               "userexample.com failed",
		"line\nbreak\ttab":                      "linebreaktab",
		"curl https://api.resend.com?token=abc": "curl https:api.resend.comtokenabc",
		"":                                      "redacted",
		"///":                                   "redacted",
	}
	for raw, want := range cases {
		if got := domain.SanitizeDetail(raw); got != want {
			t.Errorf("SanitizeDetail(%q) = %q, want %q", raw, got, want)
		}
	}
	long := strings.Repeat("a", domain.MaxErrorDetailLength+50)
	if got := domain.SanitizeDetail(long); len(got) != domain.MaxErrorDetailLength {
		t.Errorf("long detail: got %d chars, want %d", len(got), domain.MaxErrorDetailLength)
	}

	failure, err := domain.NewFailure(domain.FailureHandlerError, "dial tcp 10.0.0.1:5432: refused")
	if err != nil {
		t.Fatalf("NewFailure: %v", err)
	}
	if failure.Code != domain.FailureHandlerError {
		t.Errorf("code = %q", failure.Code)
	}
	if strings.ContainsAny(failure.Detail, "@/\\\"=") {
		t.Errorf("detail kept an unsafe character: %q", failure.Detail)
	}
}

func TestJobValidationAcceptsCoherentEntities(t *testing.T) {
	if err := validJob(t).Validate(); err != nil {
		t.Fatalf("queued job should be valid: %v", err)
	}

	leased := validJob(t)
	leased.State = domain.StateLeased
	leased.Attempts = 1
	leased.LeaseOwner = "worker-1"
	leased.LeasedUntil = leased.AvailableAt.Add(time.Minute)
	if err := leased.Validate(); err != nil {
		t.Fatalf("leased job should be valid: %v", err)
	}
	if !leased.WithinAttemptBudget() {
		t.Error("leased job still has budget")
	}
	// The lease is expired at the boundary itself (leased_until <= now), so a
	// worker holding a lease that ends exactly now has lost it.
	if leased.LeaseExpired(leased.LeasedUntil.Add(-time.Second)) {
		t.Error("lease before its expiry must still be held")
	}
	if !leased.LeaseExpired(leased.LeasedUntil) {
		t.Error("lease at its exact expiry boundary must be expired")
	}
	if !leased.LeaseExpired(leased.LeasedUntil.Add(time.Second)) {
		t.Error("lease past its expiry must be expired")
	}

	dead := validJob(t)
	dead.State = domain.StateDead
	dead.Attempts = dead.MaxAttempts
	dead.LastErrorCode = domain.FailureHandlerError
	dead.LastErrorDetail = "handler failed"
	if err := dead.Validate(); err != nil {
		t.Fatalf("dead job should be valid: %v", err)
	}
	if dead.WithinAttemptBudget() {
		t.Error("dead job has no budget left")
	}
	failure, ok := dead.Failure()
	if !ok || failure.Code != domain.FailureHandlerError {
		t.Errorf("Failure() = %v, %v", failure, ok)
	}

	keyed := validJob(t)
	keyed.IdempotencyKey = "email:account:a1:verify:1"
	if err := keyed.Validate(); err != nil {
		t.Fatalf("keyed job should be valid: %v", err)
	}
}

func TestJobValidationRejectsIncoherentEntities(t *testing.T) {
	cases := map[string]func(*domain.Job){
		"empty id":            func(j *domain.Job) { j.ID = " " },
		"unknown type":        func(j *domain.Job) { j.Type = "send_email" },
		"zero version":        func(j *domain.Job) { j.Version = 0 },
		"empty payload":       func(j *domain.Job) { j.Payload = nil },
		"wrong payload shape": func(j *domain.Job) { j.Payload = []byte(`[]`) },
		"unknown state":       func(j *domain.Job) { j.State = "pending" },
		"too many attempts":   func(j *domain.Job) { j.Attempts = j.MaxAttempts + 1 },
		"negative attempts":   func(j *domain.Job) { j.Attempts = -1 },
		"zero budget":         func(j *domain.Job) { j.MaxAttempts = 0 },
		"lease on queued": func(j *domain.Job) {
			j.LeaseOwner = "worker-1"
			j.LeasedUntil = j.AvailableAt.Add(time.Minute)
		},
		"leased without owner": func(j *domain.Job) {
			j.State = domain.StateLeased
			j.LeasedUntil = j.AvailableAt.Add(time.Minute)
		},
		"leased with zero expiry": func(j *domain.Job) {
			j.State = domain.StateLeased
			j.LeaseOwner = "worker-1"
		},
		"terminal with lease": func(j *domain.Job) {
			j.State = domain.StateSucceeded
			j.LeaseOwner = "worker-1"
			j.LeasedUntil = j.AvailableAt.Add(time.Minute)
		},
		"blank idempotency key": func(j *domain.Job) { j.IdempotencyKey = "   " },
		"error code without detail": func(j *domain.Job) {
			j.LastErrorCode = domain.FailureHandlerError
		},
		"error detail without code": func(j *domain.Job) { j.LastErrorDetail = "boom" },
		"unknown error code": func(j *domain.Job) {
			j.LastErrorCode = "JOB_NOPE"
			j.LastErrorDetail = "boom"
		},
		"zero created at": func(j *domain.Job) { j.CreatedAt = time.Time{} },
		"updated before created": func(j *domain.Job) {
			j.UpdatedAt = j.CreatedAt.Add(-time.Second)
		},
	}
	for name, mutate := range cases {
		job := validJob(t)
		mutate(&job)
		if err := job.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestLeaseValidation(t *testing.T) {
	expiry := time.Date(2026, 9, 18, 12, 1, 0, 0, time.UTC)
	if _, err := domain.NewLease("worker-1", expiry); err != nil {
		t.Fatalf("valid lease: %v", err)
	}
	if _, err := domain.NewLease("", expiry); !errors.Is(err, domain.ErrInvalidLease) {
		t.Errorf("empty owner: got %v", err)
	}
	if _, err := domain.NewLease("   ", expiry); !errors.Is(err, domain.ErrInvalidLease) {
		t.Errorf("blank owner: got %v", err)
	}
	if _, err := domain.NewLease(strings.Repeat("w", domain.MaxLeaseOwnerLength+1), expiry); !errors.Is(err, domain.ErrInvalidLease) {
		t.Errorf("oversized owner: got %v", err)
	}
	if _, err := domain.NewLease("worker-1", time.Time{}); !errors.Is(err, domain.ErrInvalidLease) {
		t.Errorf("zero expiry: got %v", err)
	}
}
