package domain_test

import (
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
)

func TestReplyPolicyAcceptsOneRecursionLevel(t *testing.T) {
	policy := domain.DefaultReplyPolicy()
	if !policy.IsValid() {
		t.Fatal("the default reply policy must be valid")
	}
	if policy.MaxDepth != 1 {
		t.Fatalf("MaxDepth = %d, want the single recursion level of REQ-ARG-02", policy.MaxDepth)
	}

	// The top-level argument is depth 0: a reply to it is depth 1 and is
	// accepted; a reply to a reply (depth 2) is not.
	if policy.AllowsChildDepth(0) {
		t.Fatal("depth 0 is the top-level argument, not a reply")
	}
	if !policy.AllowsChildDepth(1) {
		t.Fatal("depth 1 must be accepted")
	}
	if policy.AllowsChildDepth(2) || policy.AllowsChildDepth(5) {
		t.Fatal("deeper replies must be refused")
	}

	invalid := []domain.ReplyPolicy{
		{Version: "", MaxDepth: 1},
		{Version: "2026-09", MaxDepth: 0},
		{Version: "2026-09", MaxDepth: -1},
	}
	for _, candidate := range invalid {
		if candidate.IsValid() {
			t.Fatalf("policy %+v must be invalid", candidate)
		}
	}
}
