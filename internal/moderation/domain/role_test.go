package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

func TestRoleVocabulary(t *testing.T) {
	t.Parallel()

	for _, role := range []domain.Role{domain.RoleModerator, domain.RoleAdmin, domain.RoleSecurity} {
		parsed, err := domain.ParseRole(role.String())
		if err != nil || parsed != role || !parsed.IsValid() {
			t.Fatalf("ParseRole(%q) = %q, %v", role, parsed, err)
		}
	}
	if _, err := domain.ParseRole("owner"); !errors.Is(err, domain.ErrInvalidRole) {
		t.Fatalf("owner error = %v, want ErrInvalidRole", err)
	}
	if _, err := domain.ParseRole(""); !errors.Is(err, domain.ErrInvalidRole) {
		t.Fatalf("empty error = %v, want ErrInvalidRole", err)
	}
	if _, err := domain.ParseRole("moderator@arena"); !errors.Is(err, domain.ErrInvalidRole) {
		t.Fatalf("email-shaped role must not authorize: %v", err)
	}
	if len(domain.AllRoles()) != 3 {
		t.Fatalf("AllRoles has %d entries, want 3", len(domain.AllRoles()))
	}
}

func TestActionVocabularyAndImpact(t *testing.T) {
	t.Parallel()

	if len(domain.AllActions()) != 11 {
		t.Fatalf("AllActions has %d entries, want 11", len(domain.AllActions()))
	}
	for _, action := range domain.AllActions() {
		parsed, err := domain.ParseAction(action.String())
		if err != nil || parsed != action {
			t.Fatalf("ParseAction(%q) = %q, %v", action, parsed, err)
		}
	}
	if _, err := domain.ParseAction("shadow_ban"); !errors.Is(err, domain.ErrInvalidAction) {
		t.Fatalf("shadow_ban error = %v, want ErrInvalidAction", err)
	}
	for _, action := range []domain.Action{domain.ActionSuspension, domain.ActionBan, domain.ActionArenaClose, domain.ActionPreserveLegal} {
		if !action.IsHighImpact() {
			t.Errorf("%q must be high-impact", action)
		}
	}
	if domain.ActionWarning.IsHighImpact() || domain.ActionArgumentRemove.IsHighImpact() {
		t.Error("warning and argument_remove must not require step-up")
	}
}

func TestRoleMatrix(t *testing.T) {
	t.Parallel()

	// Moderators handle reversible content triage, never account sanctions.
	for _, action := range []domain.Action{
		domain.ActionNoAction, domain.ActionWarning, domain.ActionLinkHide,
		domain.ActionInteractionLimit, domain.ActionArgumentRemove,
		domain.ActionAttributionInvalidate, domain.ActionPositionInvalidate,
	} {
		if !domain.RoleModerator.MayPerform(action) {
			t.Errorf("moderator must perform %q", action)
		}
	}
	for _, action := range []domain.Action{
		domain.ActionArenaClose, domain.ActionSuspension, domain.ActionBan, domain.ActionPreserveLegal,
	} {
		if domain.RoleModerator.MayPerform(action) {
			t.Errorf("moderator must not perform %q", action)
		}
	}

	// Admins decide anything.
	for _, action := range domain.AllActions() {
		if !domain.RoleAdmin.MayPerform(action) {
			t.Errorf("admin must perform %q", action)
		}
	}

	// Security handles safety and legal preservation, not editorial removal.
	for _, action := range []domain.Action{
		domain.ActionNoAction, domain.ActionLinkHide, domain.ActionInteractionLimit,
		domain.ActionSuspension, domain.ActionPreserveLegal,
	} {
		if !domain.RoleSecurity.MayPerform(action) {
			t.Errorf("security must perform %q", action)
		}
	}
	for _, action := range []domain.Action{
		domain.ActionWarning, domain.ActionArgumentRemove, domain.ActionArenaClose,
		domain.ActionAttributionInvalidate, domain.ActionPositionInvalidate, domain.ActionBan,
	} {
		if domain.RoleSecurity.MayPerform(action) {
			t.Errorf("security must not perform %q", action)
		}
	}

	if domain.Role("owner").MayPerform(domain.ActionWarning) {
		t.Error("unknown roles must deny everything")
	}
}

func TestStepUpPolicy(t *testing.T) {
	t.Parallel()

	fresh := 5 * time.Minute
	stale := 60 * time.Minute

	if ok, err := domain.StepUpSatisfied(domain.ActionWarning, stale); err != nil || !ok {
		t.Fatalf("warning with stale session = %v, %v; want satisfied", ok, err)
	}
	if ok, err := domain.StepUpSatisfied(domain.ActionSuspension, fresh); err != nil || !ok {
		t.Fatalf("suspension with fresh session = %v, %v; want satisfied", ok, err)
	}
	if ok, err := domain.StepUpSatisfied(domain.ActionSuspension, stale); err != nil || ok {
		t.Fatalf("suspension with stale session = %v, %v; want denied", ok, err)
	}
	if ok, err := domain.StepUpSatisfied(domain.ActionBan, domain.StepUpWindow); err != nil || !ok {
		t.Fatalf("ban at the boundary = %v, %v; want satisfied", ok, err)
	}
	if _, err := domain.StepUpSatisfied(domain.ActionBan, -time.Minute); !errors.Is(err, domain.ErrInvalidSessionAge) {
		t.Fatalf("negative age error = %v, want ErrInvalidSessionAge", err)
	}
	// Zero is a valid age, not skew: both low-impact and fresh high-impact
	// sessions satisfy at the boundary (mutation gate: policy.go:117).
	if ok, err := domain.StepUpSatisfied(domain.ActionWarning, 0); err != nil || !ok {
		t.Fatalf("warning at age zero = %v, %v; want satisfied", ok, err)
	}
	if ok, err := domain.StepUpSatisfied(domain.ActionSuspension, 0); err != nil || !ok {
		t.Fatalf("suspension at age zero = %v, %v; want satisfied", ok, err)
	}
}

func TestConflictPolicyUsesIdentifiersOnly(t *testing.T) {
	t.Parallel()

	actor := domain.AccountID("018f6b2a-0000-7000-8000-000000000001")
	owner := domain.AccountID("018f6b2a-0000-7000-8000-000000000002")
	reporter := domain.AccountID("018f6b2a-0000-7000-8000-000000000003")

	if !domain.HasConflict(actor, actor, "") {
		t.Error("actor owning the target must conflict")
	}
	if !domain.HasConflict(actor, owner, actor) {
		t.Error("actor reporting the target must conflict")
	}
	if domain.HasConflict(actor, owner, reporter) {
		t.Error("uninvolved actor must not conflict")
	}
	if domain.HasConflict(actor, "", "") {
		t.Error("non-account targets without reporter must not conflict by default")
	}
}
