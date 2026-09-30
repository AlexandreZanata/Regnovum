package domain_test

import (
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

func TestSanctionMatrixPerTarget(t *testing.T) {
	t.Parallel()

	allowed := map[domain.TargetType]map[domain.Action]bool{
		domain.TargetArena: {
			domain.ActionNoAction: true, domain.ActionWarning: true,
			domain.ActionLinkHide: true, domain.ActionArenaClose: true,
			domain.ActionPositionInvalidate: true,
			domain.ActionPreserveLegal:      true,
		},
		domain.TargetArgument: {
			domain.ActionNoAction: true, domain.ActionWarning: true,
			domain.ActionLinkHide: true, domain.ActionArgumentRemove: true,
			domain.ActionAttributionInvalidate: true, domain.ActionPositionInvalidate: true,
			domain.ActionPreserveLegal: true,
		},
		domain.TargetProfile: {
			domain.ActionNoAction: true, domain.ActionWarning: true,
			domain.ActionInteractionLimit: true, domain.ActionSuspension: true,
			domain.ActionBan: true, domain.ActionPreserveLegal: true,
		},
	}

	for _, target := range domain.AllTargetTypes() {
		for _, action := range domain.AllActions() {
			want := allowed[target][action]
			if got := domain.SanctionAllowed(target, action); got != want {
				t.Errorf("SanctionAllowed(%q, %q) = %v, want %v", target, action, got, want)
			}
		}
	}

	if domain.SanctionAllowed(domain.TargetArgument, domain.ActionBan) {
		t.Error("arguments must never carry account sanctions")
	}
	if domain.SanctionAllowed(domain.TargetProfile, domain.ActionArenaClose) {
		t.Error("profiles must never carry arena sanctions")
	}
	if domain.SanctionAllowed(domain.TargetArena, domain.ActionSuspension) {
		t.Error("arenas must never carry account sanctions")
	}
}

func TestMutatingActionsAreExplicit(t *testing.T) {
	t.Parallel()

	for _, action := range []domain.Action{
		domain.ActionArenaClose, domain.ActionArgumentRemove,
		domain.ActionAttributionInvalidate, domain.ActionSuspension, domain.ActionBan,
	} {
		if !action.MutatesProjection() {
			t.Errorf("%q must mutate a projection in the decision transaction", action)
		}
	}
	for _, action := range []domain.Action{
		domain.ActionNoAction, domain.ActionWarning, domain.ActionLinkHide,
		domain.ActionInteractionLimit, domain.ActionPreserveLegal, domain.ActionPositionInvalidate,
	} {
		if action.MutatesProjection() {
			t.Errorf("%q must be recorded only, without projection mutation", action)
		}
	}
}
