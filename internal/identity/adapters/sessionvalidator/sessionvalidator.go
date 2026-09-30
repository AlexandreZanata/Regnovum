// Package sessionvalidator adapts the identity session use case to the
// platform security port a surface authenticates through (P18-T07B).
//
// It exists because two surfaces now resolve the same cookie: the account
// journey composes its own sign-in, and the Arena participation journey asks
// the platform middleware "who is this?" without importing identity. The
// session belongs to the identity module, so the adapter that answers the
// platform port lives here — the same place auditbridge and the HTTP handler's
// own adapter live — and the module that owns the rule is the module that
// exposes it.
//
// The adapter translates and decides nothing: an unknown, revoked, expired or
// foreign session is already an error in the use case vocabulary, and it
// reaches the middleware unchanged.
package sessionvalidator

import (
	"context"
	"errors"
	"fmt"

	"github.com/AlexandreZanata/Regnovum/internal/identity/application"
	"github.com/AlexandreZanata/Regnovum/internal/platform/security"
)

// Sessions is the identity capability this adapter consumes: the use case that
// resolves a raw opaque token into the account and session it belongs to.
type Sessions interface {
	Execute(ctx context.Context, command application.AuthenticateSessionCommand) (*application.AuthenticateSessionResult, error)
}

// ErrIncompleteComposition marks a refusal to adapt a session use case that is
// not there. A validator that answered "no identity" for every token would be
// indistinguishable from a working one on a read-only page and would silently
// drop authentication on a mutation, so the missing dependency is refused
// instead of absorbed.
var ErrIncompleteComposition = errors.New("sessionvalidator: incomplete composition")

// New builds the platform session validator over the identity use case.
func New(sessions Sessions) (security.SessionValidatorFunc, error) {
	if sessions == nil {
		return nil, fmt.Errorf("%w: session use case is required", ErrIncompleteComposition)
	}

	return func(ctx context.Context, rawToken string) (security.AuthIdentity, error) {
		result, err := sessions.Execute(ctx, application.AuthenticateSessionCommand{RawToken: rawToken})
		if err != nil {
			return security.AuthIdentity{}, err
		}
		if result == nil || result.Account == nil || result.Session == nil {
			// Defensive: a use case that answers without an account would
			// otherwise hand the middleware an identity nobody owns.
			return security.AuthIdentity{}, fmt.Errorf("%w: the session use case answered without an account or a session", ErrIncompleteComposition)
		}
		return security.AuthIdentity{
			AccountID: result.Account.ID().String(),
			SessionID: result.Session.ID().String(),
		}, nil
	}, nil
}
