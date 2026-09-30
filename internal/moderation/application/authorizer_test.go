package application_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

type fakeRoles struct {
	assignments map[domain.AccountID]*application.RoleAssignment
	err         error
}

func (f *fakeRoles) AssignmentFor(_ context.Context, accountID domain.AccountID) (*application.RoleAssignment, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.assignments[accountID], nil
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

const (
	actorModerator = domain.AccountID("018f6b2a-0000-7000-8000-000000000011")
	actorAdmin     = domain.AccountID("018f6b2a-0000-7000-8000-000000000012")
	actorSecurity  = domain.AccountID("018f6b2a-0000-7000-8000-000000000013")
	actorNobody    = domain.AccountID("018f6b2a-0000-7000-8000-000000000014")
	actorRevoked   = domain.AccountID("018f6b2a-0000-7000-8000-000000000015")
	targetOwner    = domain.AccountID("018f6b2a-0000-7000-8000-000000000021")
)

func authorizerFixture() (*application.Authorizer, *fakeRoles) {
	roles := &fakeRoles{assignments: map[domain.AccountID]*application.RoleAssignment{
		actorModerator: {AccountID: actorModerator, Role: domain.RoleModerator},
		actorAdmin:     {AccountID: actorAdmin, Role: domain.RoleAdmin},
		actorSecurity:  {AccountID: actorSecurity, Role: domain.RoleSecurity},
		actorRevoked:   {AccountID: actorRevoked, Role: domain.RoleModerator, Revoked: true},
	}}
	authorizer, err := application.NewAuthorizer(roles, &fakeClock{})
	if err != nil {
		panic(err)
	}
	return authorizer, roles
}

func TestAuthorizerMatrixAllowDeny(t *testing.T) {
	t.Parallel()

	authorizer, _ := authorizerFixture()
	fresh := 5 * time.Minute

	allowed := []struct {
		actor  domain.AccountID
		action domain.Action
	}{
		{actorModerator, domain.ActionWarning},
		{actorModerator, domain.ActionArgumentRemove},
		{actorAdmin, domain.ActionBan},
		{actorAdmin, domain.ActionArenaClose},
		{actorAdmin, domain.ActionSuspension},
		{actorSecurity, domain.ActionPreserveLegal},
		{actorSecurity, domain.ActionSuspension},
	}
	for _, tc := range allowed {
		result, err := authorizer.EnsureAuthorized(context.Background(), application.AuthorizeCommand{
			Actor:      tc.actor,
			Action:     tc.action,
			SessionAge: fresh,
		})
		if err != nil {
			t.Errorf("%s may take %s: %v", tc.actor, tc.action, err)
		} else if result.Conflict {
			t.Errorf("%s taking %s unexpectedly flagged conflict", tc.actor, tc.action)
		}
	}

	denied := []struct {
		actor  domain.AccountID
		action domain.Action
	}{
		{actorModerator, domain.ActionBan},
		{actorModerator, domain.ActionSuspension},
		{actorModerator, domain.ActionArenaClose},
		{actorSecurity, domain.ActionBan},
		{actorSecurity, domain.ActionArgumentRemove},
		{actorNobody, domain.ActionWarning},
	}
	for _, tc := range denied {
		if _, err := authorizer.EnsureAuthorized(context.Background(), application.AuthorizeCommand{
			Actor:      tc.actor,
			Action:     tc.action,
			SessionAge: fresh,
		}); err == nil {
			t.Errorf("%s taking %s must deny", tc.actor, tc.action)
		}
	}
}

func TestAuthorizerIgnoresPaymentAndPopularity(t *testing.T) {
	t.Parallel()

	// The command carries no payment, subscription or popularity input by
	// construction: the same actor, action and freshness decide identically
	// whatever the target's commercial state. Two unrelated targets stand in
	// for "paid" and "free" accounts.
	authorizer, _ := authorizerFixture()
	fresh := 5 * time.Minute

	paidTarget := domain.AccountID("018f6b2a-0000-7000-8000-000000000031")
	freeTarget := domain.AccountID("018f6b2a-0000-7000-8000-000000000032")
	for _, target := range []domain.AccountID{paidTarget, freeTarget} {
		if _, err := authorizer.EnsureAuthorized(context.Background(), application.AuthorizeCommand{
			Actor:       actorAdmin,
			Action:      domain.ActionSuspension,
			TargetOwner: target,
			SessionAge:  fresh,
		}); err != nil {
			t.Fatalf("target %s: %v", target, err)
		}
	}

	// Mechanical proof: no field of the command may carry commercial state.
	commandType := reflect.TypeOf(application.AuthorizeCommand{})
	for i := 0; i < commandType.NumField(); i++ {
		name := strings.ToLower(commandType.Field(i).Name)
		for _, marker := range []string{"paid", "popular", "billing", "subscription", "price", "email", "frontend"} {
			if strings.Contains(name, marker) {
				t.Fatalf("AuthorizeCommand.%s carries %q", commandType.Field(i).Name, marker)
			}
		}
	}
}

func TestAuthorizerRevokedRoleDeniesActiveSession(t *testing.T) {
	t.Parallel()

	authorizer, _ := authorizerFixture()

	_, err := authorizer.EnsureAuthorized(context.Background(), application.AuthorizeCommand{
		Actor:      actorRevoked,
		Action:     domain.ActionWarning,
		SessionAge: 5 * time.Minute,
	})
	if !errors.Is(err, application.ErrRoleRevoked) {
		t.Fatalf("revoked error = %v, want ErrRoleRevoked", err)
	}
}

func TestAuthorizerConflictAndStepUp(t *testing.T) {
	t.Parallel()

	authorizer, _ := authorizerFixture()

	_, err := authorizer.EnsureAuthorized(context.Background(), application.AuthorizeCommand{
		Actor:       actorModerator,
		Action:      domain.ActionWarning,
		TargetOwner: actorModerator,
		SessionAge:  5 * time.Minute,
	})
	if !errors.Is(err, application.ErrConflictOfInterest) {
		t.Fatalf("self-review error = %v, want ErrConflictOfInterest", err)
	}

	_, err = authorizer.EnsureAuthorized(context.Background(), application.AuthorizeCommand{
		Actor:      actorAdmin,
		Action:     domain.ActionBan,
		SessionAge: 60 * time.Minute,
	})
	if !errors.Is(err, application.ErrStepUpRequired) {
		t.Fatalf("stale high-impact error = %v, want ErrStepUpRequired", err)
	}

	if _, err := authorizer.EnsureAuthorized(context.Background(), application.AuthorizeCommand{
		Actor:      actorModerator,
		Action:     domain.ActionWarning,
		SessionAge: 60 * time.Minute,
	}); err != nil {
		t.Fatalf("stale low-impact must stay allowed: %v", err)
	}
}

func TestAuthorizerRejectsIncompleteCompositionAndInput(t *testing.T) {
	t.Parallel()

	if _, err := application.NewAuthorizer(nil, &fakeClock{}); err == nil {
		t.Fatal("nil roles must refuse composition")
	}
	authorizer, _ := authorizerFixture()
	if _, err := authorizer.EnsureAuthorized(context.Background(), application.AuthorizeCommand{
		Action:     domain.ActionWarning,
		SessionAge: time.Minute,
	}); err == nil {
		t.Fatal("empty actor must deny")
	}
}
