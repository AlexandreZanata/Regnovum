// Package administrationtarget answers the moderation module's question "which
// account does this address name, and what is true of it?" (P19-T09).
//
// The moderation module states the facts it needs to decide a promotion — the
// account, whether its address was proved, whether it can sign in and whether
// it holds a confirmed second factor — and this adapter is the only place where
// the account and the second factor are read. The module that owns the account
// schema exposes the adapter, exactly as the session validator exposes the
// platform's identity port and the audit bridges expose the trail.
//
// It translates and decides nothing. Every refusal that depends on a fact is
// made by the use case, in the moderation module's own vocabulary, so the
// command's refusals are one list in one place; here the only decisions are
// about representation — an address that does not parse, an account that does
// not exist, and an enrollment that does not exist yet, which is a fact rather
// than a failure.
//
// The second factor is read as an enrollment, never as a secret: the record
// carries the sealed ciphertext and the confirmation instant, and only the
// instant is looked at. No secret, no code and no recovery material crosses
// this boundary.
package administrationtarget

import (
	"context"
	"errors"
	"fmt"

	identityapp "github.com/AlexandreZanata/Regnovum/internal/identity/application"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	moderationapp "github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	moderationdomain "github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

// ErrIncompleteComposition marks the refusal to build the adapter over a
// missing capability. A directory that answered "no such account" because it
// was never wired would refuse a legitimate promotion with a message that
// accuses the operator, so the missing dependency is refused where it is
// composed.
var ErrIncompleteComposition = errors.New("administrationtarget: incomplete composition")

// Accounts is the identity capability this adapter consumes: the lookup of an
// account by its address.
type Accounts interface {
	// GetAccountByEmail returns identityapp.ErrAccountNotFound for an address
	// that identifies no account.
	GetAccountByEmail(ctx context.Context, email identitydomain.Email) (*identitydomain.Account, error)
}

// Enrollments is the second-factor capability this adapter consumes.
type Enrollments interface {
	// GetMFAEnrollment returns identityapp.ErrMFAEnrollmentMissing when the
	// account never started an enrollment.
	GetMFAEnrollment(ctx context.Context, accountID identitydomain.AccountID) (*identityapp.MFAEnrollmentRecord, error)
}

// Directory implements moderationapp.AdministrationTargetDirectory over the
// identity repositories.
type Directory struct {
	accounts    Accounts
	enrollments Enrollments
}

var _ moderationapp.AdministrationTargetDirectory = (*Directory)(nil)

// NewDirectory wires the adapter. Both capabilities are required: an address
// resolves to an account and the promotion needs the second factor, so a
// directory built without either would either promote on an unproved address or
// promote without the factor the administrative gate demands.
func NewDirectory(accounts Accounts, enrollments Enrollments) (*Directory, error) {
	if accounts == nil || enrollments == nil {
		return nil, fmt.Errorf("%w: the account lookup and the second factor are both required", ErrIncompleteComposition)
	}
	return &Directory{accounts: accounts, enrollments: enrollments}, nil
}

// LookupByEmail resolves the address into the facts a promotion is decided on.
func (d *Directory) LookupByEmail(ctx context.Context, email string) (*moderationapp.AdministrationTarget, error) {
	if d == nil || d.accounts == nil || d.enrollments == nil {
		return nil, fmt.Errorf("%w: the directory was not wired", ErrIncompleteComposition)
	}
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrIncompleteComposition)
	}

	address, err := identitydomain.ParseEmail(email)
	if err != nil {
		// The domain error names the syntax, never the address. The
		// refusal is the moderation module's, so a caller of this port
		// reads one vocabulary whatever the shape of the input.
		return nil, fmt.Errorf("%w: %s", moderationapp.ErrInvalidAdministrationTarget, err)
	}

	account, err := d.accounts.GetAccountByEmail(ctx, address)
	if err != nil {
		if errors.Is(err, identityapp.ErrAccountNotFound) {
			return nil, moderationapp.ErrAdministrationTargetNotFound
		}
		return nil, fmt.Errorf("resolve the administration target: %w", err)
	}
	if account == nil {
		// Defensive: an implementation that answers neither an account
		// nor an error would otherwise hand the use case a nil it does
		// not expect.
		return nil, moderationapp.ErrAdministrationTargetNotFound
	}

	confirmed, err := d.secondFactorConfirmed(ctx, account.ID())
	if err != nil {
		return nil, err
	}

	return &moderationapp.AdministrationTarget{
		AccountID:             moderationdomain.AccountID(account.ID().String()),
		EmailVerified:         account.IsVerified(),
		CanAuthenticate:       account.CanAuthenticate(),
		SecondFactorConfirmed: confirmed,
	}, nil
}

// secondFactorConfirmed reports whether the account proved the second factor.
// A missing enrollment is a fact — the account never started one — and travels
// as false; anything else that fails is an error, because "the second factor is
// unreadable" must not be answered with "the second factor is absent" and turn
// a broken read into a wrong refusal.
func (d *Directory) secondFactorConfirmed(ctx context.Context, accountID identitydomain.AccountID) (bool, error) {
	enrollment, err := d.enrollments.GetMFAEnrollment(ctx, accountID)
	switch {
	case err == nil:
		return enrollment != nil && enrollment.ConfirmedAt != nil, nil
	case errors.Is(err, identityapp.ErrMFAEnrollmentMissing):
		return false, nil
	default:
		return false, fmt.Errorf("resolve the administration target: %w", err)
	}
}
