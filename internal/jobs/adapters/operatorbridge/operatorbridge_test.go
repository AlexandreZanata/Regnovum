package operatorbridge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/operatorbridge"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	moderationdomain "github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

// roleStub answers the one question the bridge asks. It embeds the port so the
// test fails to compile if that port ever grows a method the bridge does not
// know about.
type roleStub struct {
	moderationapp.RoleRepository
	assignment *moderationapp.RoleAssignment
	err        error
	asked      moderationdomain.AccountID
}

func (s *roleStub) AssignmentFor(_ context.Context, accountID moderationdomain.AccountID) (*moderationapp.RoleAssignment, error) {
	s.asked = accountID
	if s.err != nil {
		return nil, s.err
	}
	return s.assignment, nil
}

func directoryFor(t *testing.T, roles moderationapp.RoleRepository) *operatorbridge.Directory {
	t.Helper()
	directory, err := operatorbridge.NewDirectory(roles)
	if err != nil {
		t.Fatalf("NewDirectory() error = %v", err)
	}
	return directory
}

func assignment(role moderationdomain.Role, revoked bool) *moderationapp.RoleAssignment {
	return &moderationapp.RoleAssignment{
		AccountID: moderationdomain.AccountID("0191f0e0-0000-7000-8000-0000000000aa"),
		Role:      role,
		Revoked:   revoked,
	}
}

// TestOnlyAnActiveAdministrativeAssignmentIsAnOperator is the gate: the
// operational surface is administrative, so the moderator and security roles
// triage content and do not reach the queue.
func TestOnlyAnActiveAdministrativeAssignmentIsAnOperator(t *testing.T) {
	cases := []struct {
		name       string
		assignment *moderationapp.RoleAssignment
		want       bool
	}{
		{name: "admin", assignment: assignment(moderationdomain.RoleAdmin, false), want: true},
		{name: "moderator", assignment: assignment(moderationdomain.RoleModerator, false), want: false},
		{name: "security", assignment: assignment(moderationdomain.RoleSecurity, false), want: false},
		{name: "revoked admin", assignment: assignment(moderationdomain.RoleAdmin, true), want: false},
		{name: "no assignment", assignment: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			roles := &roleStub{assignment: tc.assignment}
			allowed, err := directoryFor(t, roles).IsOperator(context.Background(), "0191f0e0-0000-7000-8000-0000000000aa")
			if err != nil {
				t.Fatalf("IsOperator() error = %v", err)
			}
			if allowed != tc.want {
				t.Errorf("IsOperator() = %v, want %v", allowed, tc.want)
			}
			if roles.asked != "0191f0e0-0000-7000-8000-0000000000aa" {
				t.Errorf("asked for %q, want the account identifier", roles.asked)
			}
		})
	}
}

// TestUnknownAndRevokedAnswerIdentically keeps the gate from oracles: a
// caller cannot use the answer to learn who holds what.
func TestUnknownAndRevokedAnswerIdentically(t *testing.T) {
	never := &roleStub{}
	revoked := &roleStub{assignment: assignment(moderationdomain.RoleAdmin, true)}
	first, err := directoryFor(t, never).IsOperator(context.Background(), "acc")
	if err != nil {
		t.Fatalf("IsOperator() error = %v", err)
	}
	second, err := directoryFor(t, revoked).IsOperator(context.Background(), "acc")
	if err != nil {
		t.Fatalf("IsOperator() error = %v", err)
	}
	if first != second || first {
		t.Errorf("never/revoked = %v/%v, want the same denial", first, second)
	}
}

// TestAnUnanswerableQuestionIsAnError: a store that cannot answer must not be
// able to say yes.
func TestAnUnanswerableQuestionIsAnError(t *testing.T) {
	failure := errors.New("roles: unavailable")
	roles := &roleStub{err: failure}
	allowed, err := directoryFor(t, roles).IsOperator(context.Background(), "acc")
	if !errors.Is(err, failure) {
		t.Errorf("IsOperator() error = %v, want the store failure", err)
	}
	if allowed {
		t.Error("IsOperator() = true on a failed lookup")
	}
}

func TestDirectoryRequiresTheRoleStore(t *testing.T) {
	if _, err := operatorbridge.NewDirectory(nil); err == nil {
		t.Fatal("NewDirectory(nil) error = nil, want a failure")
	}
	var unwired *operatorbridge.Directory
	allowed, err := unwired.IsOperator(context.Background(), "acc")
	if err == nil {
		t.Error("IsOperator() on an unwired bridge error = nil, want a failure")
	}
	if allowed {
		t.Error("IsOperator() = true without a wired store")
	}
}
