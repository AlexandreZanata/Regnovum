// Local administration of the installation (P19-T09).
//
// An installation has no administrator until one is promoted, and the act
// belongs to an operator on the host: the process that runs it holds the
// database DSN, and no route exists for it — the moderation module exposes no
// HTTP surface that grants a role, and `internal/bootstrap` is the only place
// that composes the use case that does. This file is that composition.
//
// The composition is deliberately separate from the server's: `arena server`
// and `arena worker` never build it, because serving requests and promoting an
// administrator are different powers. A process that can answer a page should
// not hold the ability to create the first administrator, and the way to make
// that true is for the code to not be there.
package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"

	auditpostgres "github.com/AlexandreZanata/Regnovum/internal/audit/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/administrationtarget"
	identitypostgres "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/postgres"
	moderationauditbridge "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/auditbridge"
	moderationpostgres "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/postgres"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	platformpg "github.com/AlexandreZanata/Regnovum/internal/platform/postgres"
)

// Administration is the composed local administration: the promotion of the
// first administrator and the demotion that reverses it.
//
// Both halves are composed together because they are one capability seen from
// two directions. A promotion nothing can undo would leave an installation with
// an administrator it cannot remove, and the phase's gate asks for a stack that
// is reversible as well as reproducible.
type Administration struct {
	grant  *moderationapp.GrantFirstAdministratorUseCase
	revoke *moderationapp.RevokeAdministratorUseCase
}

// ComposeAdministration builds the local administration over the process pool.
//
// It reads the logger, the pool and the clock and ignores every other field of
// Options: a command that runs on the host has no assets, no cursor secret and
// no email provider, and requiring them would tie the recovery path of the
// installation to a deployment's configuration.
//
// Everything the promotion needs is composed here, which is what makes the
// trail structural rather than optional: the audit bridge is wired before the
// use case exists, so there is no ordering in which an unrecorded promotion
// could be built.
func ComposeAdministration(options Options) (*Administration, error) {
	if err := validateAdministration(options); err != nil {
		return nil, err
	}

	identity := identitypostgres.NewRepository(options.Pool)
	targets, err := administrationtarget.NewDirectory(identity, identity)
	if err != nil {
		return nil, fmt.Errorf("%w: local administration: %w", ErrIncompleteComposition, err)
	}

	// One repository answers the reads and the writes of the assignment
	// schema; the lock that serializes "the installation has no
	// administrator" and the insert that follows it belongs to the same
	// adapter, inside the caller transaction.
	assignments := moderationpostgres.NewRepository(options.Pool)

	audit, err := moderationauditbridge.NewRecorder(auditpostgres.NewRepository(options.Pool))
	if err != nil {
		return nil, fmt.Errorf("%w: local administration: %w", ErrIncompleteComposition, err)
	}

	// The transaction manager is what makes the decision and its record one
	// fact: the lock, the check, the write and the audit event commit
	// together or the promotion does not happen.
	transactions := platformpg.NewTxManager(options.Pool)

	grant, err := moderationapp.NewGrantFirstAdministratorUseCase(targets, assignments, assignments, audit, options.Clock, transactions)
	if err != nil {
		return nil, fmt.Errorf("%w: local administration: %w", ErrIncompleteComposition, err)
	}
	revoke, err := moderationapp.NewRevokeAdministratorUseCase(targets, assignments, assignments, audit, options.Clock, transactions)
	if err != nil {
		return nil, fmt.Errorf("%w: local administration: %w", ErrIncompleteComposition, err)
	}

	options.Logger.Info(
		"local administration: composed; the first administrator is promoted and demoted from the host, never over HTTP",
	)
	return &Administration{grant: grant, revoke: revoke}, nil
}

// GrantFirstAdministrator promotes the account the address belongs to, or
// refuses naming the rule that refused.
func (a *Administration) GrantFirstAdministrator(ctx context.Context, email string) (*moderationapp.GrantFirstAdministratorResult, error) {
	if a == nil || a.grant == nil {
		return nil, fmt.Errorf("%w: local administration: the promotion was not composed", ErrIncompleteComposition)
	}
	return a.grant.Execute(ctx, moderationapp.GrantFirstAdministratorCommand{Email: email})
}

// RevokeAdministrator reverses a promotion, recording it in the same trail.
func (a *Administration) RevokeAdministrator(ctx context.Context, email string) (*moderationapp.RevokeAdministratorResult, error) {
	if a == nil || a.revoke == nil {
		return nil, fmt.Errorf("%w: local administration: the demotion was not composed", ErrIncompleteComposition)
	}
	return a.revoke.Execute(ctx, moderationapp.RevokeAdministratorCommand{Email: email})
}

// validateAdministration reports every missing edge at once, sorted, for the
// same reason the journeys do: an operator fixing a failure should not discover
// the missing pieces one per attempt.
func validateAdministration(options Options) error {
	missing := make([]string, 0, 3)
	if options.Logger == nil {
		missing = append(missing, "logger")
	}
	if options.Clock == nil {
		missing = append(missing, "clock")
	}
	if options.Pool == nil {
		missing = append(missing, "postgres pool")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: local administration: missing %s", ErrIncompleteComposition, strings.Join(missing, ", "))
	}
	return nil
}
