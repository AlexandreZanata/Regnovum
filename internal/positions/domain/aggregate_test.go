package domain_test

import (
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

func TestAggregatePolicyThreshold(t *testing.T) {
	policy := domain.DefaultAggregatePolicy()
	if !policy.IsValid() {
		t.Fatal("the default policy must be valid")
	}
	if policy.MinParticipants < 1 {
		t.Fatalf("default threshold = %d, want a positive sample floor", policy.MinParticipants)
	}

	if policy.Suppresses(policy.MinParticipants-1) != true {
		t.Fatal("a population below the threshold must be suppressed")
	}
	if policy.Suppresses(policy.MinParticipants) {
		t.Fatal("a population at the threshold must be published")
	}

	invalid := []domain.AggregatePolicy{
		{Version: "", MinParticipants: 10},
		{Version: "2026-09", MinParticipants: 0},
		{Version: "2026-09", MinParticipants: -1},
	}
	for _, candidate := range invalid {
		if candidate.IsValid() {
			t.Fatalf("policy %+v must be invalid", candidate)
		}
	}
}
