package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// PublicProfile is the explicit public projection of a profile: exactly the
// fields allowed in public responses. It never carries email, credentials,
// internal financial identifiers, antifraud flags or administrative notes
// (docs/PRIVACY.md).
type PublicProfile struct {
	Username        string
	InterfaceLocale string
	CreatedAt       time.Time
}

// PrivateProfile is the explicit projection returned to the owner of the
// profile. It adds only the mutation timestamp to the public fields; account
// and payment identifiers stay in their owning modules.
type PrivateProfile struct {
	Username        string
	InterfaceLocale string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ProfileQueryRepository exposes the separated read projections of profiles.
// Public lookups resolve exclusively by the canonical normalized username;
// private lookups resolve by the owning account.
type ProfileQueryRepository interface {
	// GetPublicProfileByUsername returns the public projection of the profile
	// that owns the normalized username, or ErrProfileNotFound.
	GetPublicProfileByUsername(ctx context.Context, normalizedUsername string) (*PublicProfile, error)

	// GetPrivateProfileByAccountID returns the owner projection, or
	// ErrProfileNotFound.
	GetPrivateProfileByAccountID(ctx context.Context, accountID domain.AccountID) (*PrivateProfile, error)
}
