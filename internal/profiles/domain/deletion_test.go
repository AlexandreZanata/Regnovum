package domain_test

import (
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

var deletionBase = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func TestDeletionCooldownAndExecutability(t *testing.T) {
	t.Parallel()

	if domain.DeletionCooldown != 7*24*time.Hour {
		t.Fatalf("DeletionCooldown = %s, want 168h", domain.DeletionCooldown)
	}

	cases := []struct {
		name        string
		requestedAt time.Time
		now         time.Time
		want        bool
	}{
		{name: "inside cooldown", requestedAt: deletionBase, now: deletionBase.Add(time.Hour), want: false},
		{name: "boundary instant", requestedAt: deletionBase, now: deletionBase.Add(domain.DeletionCooldown), want: true},
		{name: "past cooldown", requestedAt: deletionBase, now: deletionBase.Add(domain.DeletionCooldown + time.Second), want: true},
		{name: "before request", requestedAt: deletionBase, now: deletionBase.Add(-time.Second), want: false},
		{name: "zero request", requestedAt: time.Time{}, now: deletionBase, want: false},
		{name: "zero now", requestedAt: deletionBase, now: time.Time{}, want: false},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := domain.DeletionExecutable(testCase.requestedAt, testCase.now); got != testCase.want {
				t.Fatalf("DeletionExecutable = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestDeletionCancellableStates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		status      domain.DeletionRequestStatus
		now         time.Time
		requestedAt time.Time
		want        bool
	}{
		{name: "requested inside window", status: domain.DeletionStatusRequested, now: deletionBase.Add(time.Hour), requestedAt: deletionBase, want: true},
		{name: "requested at boundary", status: domain.DeletionStatusRequested, now: deletionBase.Add(domain.DeletionCooldown), requestedAt: deletionBase, want: false},
		{name: "requested past window", status: domain.DeletionStatusRequested, now: deletionBase.Add(domain.DeletionCooldown + time.Hour), requestedAt: deletionBase, want: false},
		{name: "executed", status: domain.DeletionStatusExecuted, now: deletionBase.Add(time.Hour), requestedAt: deletionBase, want: false},
		{name: "canceled", status: domain.DeletionStatusCanceled, now: deletionBase.Add(time.Hour), requestedAt: deletionBase, want: false},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := domain.DeletionCancellable(testCase.status, testCase.now, testCase.requestedAt); got != testCase.want {
				t.Fatalf("DeletionCancellable = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestAnonymizationPolicyIsStable(t *testing.T) {
	t.Parallel()

	if domain.AnonymizationPolicy != "unresolvable_author" {
		t.Fatalf("AnonymizationPolicy = %q", domain.AnonymizationPolicy)
	}
}
