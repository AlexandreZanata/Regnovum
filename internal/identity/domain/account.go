package domain

import (
	"strings"
	"time"
)

// AccountID uniquely identifies an account within the Regnovum platform.
type AccountID string

// String returns the string representation of the account identifier.
func (id AccountID) String() string {
	return string(id)
}

// IsZero reports whether the AccountID is uninitialized.
func (id AccountID) IsZero() bool {
	return strings.TrimSpace(string(id)) == ""
}

// AccountStatus represents the discrete lifecycle states of an account.
type AccountStatus string

const (
	AccountStatusPending   AccountStatus = "pending"
	AccountStatusActive    AccountStatus = "active"
	AccountStatusSuspended AccountStatus = "suspended"
	AccountStatusDeleted   AccountStatus = "deleted"
)

// IsValid reports whether the account status is an authorized enum value.
func (s AccountStatus) IsValid() bool {
	switch s {
	case AccountStatusPending, AccountStatusActive, AccountStatusSuspended, AccountStatusDeleted:
		return true
	default:
		return false
	}
}

// Account is the root entity of the account aggregate in the identity domain.
type Account struct {
	id              AccountID
	email           Email
	status          AccountStatus
	emailVerifiedAt *time.Time
	createdAt       time.Time
	updatedAt       time.Time
}

// NewAccount initializes a new account in the Pending state.
func NewAccount(id AccountID, email Email, now time.Time) (*Account, error) {
	if id.IsZero() {
		return nil, ErrEmptyAccountID
	}
	if email.IsZero() {
		return nil, ErrEmptyEmail
	}

	return &Account{
		id:              id,
		email:           email,
		status:          AccountStatusPending,
		emailVerifiedAt: nil,
		createdAt:       now,
		updatedAt:       now,
	}, nil
}

// ReconstituteAccount builds an Account entity from persistent state without re-running creation logic.
func ReconstituteAccount(
	id AccountID,
	email Email,
	status AccountStatus,
	emailVerifiedAt *time.Time,
	createdAt, updatedAt time.Time,
) (*Account, error) {
	if id.IsZero() {
		return nil, ErrEmptyAccountID
	}
	if email.IsZero() {
		return nil, ErrEmptyEmail
	}
	if !status.IsValid() {
		return nil, ErrInvalidAccountStatus
	}

	var verifiedCopy *time.Time
	if emailVerifiedAt != nil {
		t := *emailVerifiedAt
		verifiedCopy = &t
	}

	return &Account{
		id:              id,
		email:           email,
		status:          status,
		emailVerifiedAt: verifiedCopy,
		createdAt:       createdAt,
		updatedAt:       updatedAt,
	}, nil
}

// ID returns the unique identifier of the account.
func (a *Account) ID() AccountID {
	return a.id
}

// Email returns the normalized email value object of the account.
func (a *Account) Email() Email {
	return a.email
}

// Status returns the current lifecycle status of the account.
func (a *Account) Status() AccountStatus {
	return a.status
}

// EmailVerifiedAt returns a copy of the verification instant, or nil if unverified.
func (a *Account) EmailVerifiedAt() *time.Time {
	if a.emailVerifiedAt == nil {
		return nil
	}
	t := *a.emailVerifiedAt
	return &t
}

// CreatedAt returns the account creation timestamp.
func (a *Account) CreatedAt() time.Time {
	return a.createdAt
}

// UpdatedAt returns the timestamp of the latest state mutation.
func (a *Account) UpdatedAt() time.Time {
	return a.updatedAt
}

// IsVerified reports whether the account email has been verified.
func (a *Account) IsVerified() bool {
	return a.emailVerifiedAt != nil
}

// CanAuthenticate reports whether the account is permitted to log in.
// Only accounts in the Active state can authenticate.
func (a *Account) CanAuthenticate() bool {
	return a.status == AccountStatusActive
}

// IsEligibleForVerification reports whether email verification can proceed.
// Only Pending accounts are eligible for initial verification.
func (a *Account) IsEligibleForVerification() bool {
	return a.status == AccountStatusPending
}

// VerifyEmail transitions the account from Pending to Active upon successful verification.
func (a *Account) VerifyEmail(now time.Time) error {
	switch a.status {
	case AccountStatusDeleted:
		return ErrAccountDeleted
	case AccountStatusSuspended:
		return ErrAccountSuspended
	case AccountStatusActive:
		return ErrAccountAlreadyVerified
	case AccountStatusPending:
		verifiedAt := now
		a.status = AccountStatusActive
		a.emailVerifiedAt = &verifiedAt
		a.updatedAt = now
		return nil
	default:
		return ErrInvalidAccountTransition
	}
}

// Suspend transitions an Active account to Suspended as a result of moderation.
func (a *Account) Suspend(now time.Time) error {
	switch a.status {
	case AccountStatusDeleted:
		return ErrAccountDeleted
	case AccountStatusActive:
		a.status = AccountStatusSuspended
		a.updatedAt = now
		return nil
	default:
		return ErrInvalidAccountTransition
	}
}

// Unsuspend restores a Suspended account back to Active upon appeal or expiration.
func (a *Account) Unsuspend(now time.Time) error {
	switch a.status {
	case AccountStatusDeleted:
		return ErrAccountDeleted
	case AccountStatusSuspended:
		a.status = AccountStatusActive
		a.updatedAt = now
		return nil
	default:
		return ErrInvalidAccountTransition
	}
}

// MarkDeleted transitions the account to the terminal Deleted state.
func (a *Account) MarkDeleted(now time.Time) error {
	if a.status == AccountStatusDeleted {
		return ErrAccountDeleted
	}
	a.status = AccountStatusDeleted
	a.updatedAt = now
	return nil
}

// ChangeEmail updates the email of an account and resets verification status to Pending.
func (a *Account) ChangeEmail(newEmail Email, now time.Time) error {
	if a.status == AccountStatusDeleted {
		return ErrAccountDeleted
	}
	if a.status == AccountStatusSuspended {
		return ErrAccountSuspended
	}
	if newEmail.IsZero() {
		return ErrEmptyEmail
	}

	a.email = newEmail
	a.status = AccountStatusPending
	a.emailVerifiedAt = nil
	a.updatedAt = now
	return nil
}
