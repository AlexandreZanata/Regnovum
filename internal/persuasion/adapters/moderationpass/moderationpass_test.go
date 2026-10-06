package moderationpass_test

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
	"github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/moderationpass"
	persuasionapp "github.com/AlexandreZanata/Regnovum/internal/persuasion/application"
	persuasiondomain "github.com/AlexandreZanata/Regnovum/internal/persuasion/domain"
)

// fakeRoles is an in-memory moderation role store: nil for unknown accounts.
type fakeRoles struct {
	assignments map[string]*application.RoleAssignment
	err         error
}

func (f *fakeRoles) AssignmentFor(_ context.Context, accountID domain.AccountID) (*application.RoleAssignment, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.assignments[string(accountID)], nil
}

func moderatorID(t *testing.T, raw string) persuasiondomain.ModeratorID {
	t.Helper()

	parsed, err := persuasiondomain.ParseModeratorID(raw)
	if err != nil {
		t.Fatalf("ParseModeratorID(%q) error = %v", raw, err)
	}
	return parsed
}

func TestEnsureModeratorAllowsActiveAssignment(t *testing.T) {
	t.Parallel()

	bridge := moderationpass.New(&fakeRoles{assignments: map[string]*application.RoleAssignment{
		"moderator-account": {AccountID: "moderator-account", Role: domain.RoleModerator},
	}})
	if err := bridge.EnsureModerator(context.Background(), moderatorID(t, "moderator-account")); err != nil {
		t.Fatalf("EnsureModerator() error = %v, want the active assignment allowed", err)
	}
}

func TestEnsureModeratorDeniesWithoutAssignment(t *testing.T) {
	t.Parallel()

	bridge := moderationpass.New(&fakeRoles{})
	if err := bridge.EnsureModerator(context.Background(), moderatorID(t, "unknown-account")); !errors.Is(err, persuasionapp.ErrNotAuthorized) {
		t.Fatalf("EnsureModerator() error = %v, want ErrNotAuthorized", err)
	}
}

func TestEnsureModeratorDeniesRevokedAssignment(t *testing.T) {
	t.Parallel()

	bridge := moderationpass.New(&fakeRoles{assignments: map[string]*application.RoleAssignment{
		"revoked-account": {AccountID: "revoked-account", Role: domain.RoleModerator, Revoked: true},
	}})
	if err := bridge.EnsureModerator(context.Background(), moderatorID(t, "revoked-account")); !errors.Is(err, persuasionapp.ErrNotAuthorized) {
		t.Fatalf("EnsureModerator() error = %v, want ErrNotAuthorized", err)
	}
}

func TestEnsureModeratorPropagatesStoreErrors(t *testing.T) {
	t.Parallel()

	storeErr := errors.New("role store is unavailable")
	bridge := moderationpass.New(&fakeRoles{err: storeErr})
	if err := bridge.EnsureModerator(context.Background(), moderatorID(t, "any-account")); !errors.Is(err, storeErr) {
		t.Fatalf("EnsureModerator() error = %v, want the store failure, not a denial", err)
	}
}
