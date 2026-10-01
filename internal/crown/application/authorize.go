package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

// ReignResolver is the consumer port for invested authority: it
// answers which holder and reign one season currently invests, with
// the book window and whether the book is open. Holder selection
// arrives in P47; this port only carries the snapshot, never the
// algorithm. Implementations stay out of production wiring until
// the release gate: tests stand in with a fake.
type ReignResolver interface {
	// Current returns the invested snapshot of one season book. An
	// unknown book refuses with domain.ErrSeasonMismatch; a
	// diverged snapshot refuses with domain.ErrInvalidAuthority.
	Current(ctx context.Context, season domain.SeasonID) (domain.CurrentReign, error)
}

// AuthorizeCommand carries one act claim with its session, an
// optional delegation chain, and the call clock and session cap.
// Raw tokens fail validation before the port is read: shapeless
// claims never reach storage.
type AuthorizeCommand struct {
	Act           string
	Season        string
	Holder        string
	Competence    string
	Reign         int
	Operator      bool
	Session       domain.SovereignSession
	Delegation    *domain.Delegation
	Now           time.Time
	MaxSessionAge time.Duration
}

// AuthorizeUseCase judges sovereign acts against the invested
// snapshot. It moves nothing and wires nothing: it parses, loads
// the current reign once, and defers every rule to the domain.
type AuthorizeUseCase struct {
	reigns ReignResolver
}

// NewAuthorizeUseCase creates an instance of AuthorizeUseCase.
func NewAuthorizeUseCase(reigns ReignResolver) *AuthorizeUseCase {
	return &AuthorizeUseCase{reigns: reigns}
}

// Execute authorizes one act or refuses before any effect.
func (uc *AuthorizeUseCase) Execute(ctx context.Context, cmd AuthorizeCommand) (domain.Grant, error) {
	act, err := domain.ParseActID(cmd.Act)
	if err != nil {
		return domain.Grant{}, err
	}
	season, err := domain.ParseSeasonID(cmd.Season)
	if err != nil {
		return domain.Grant{}, err
	}
	holder, err := domain.ParseHolderSubject(cmd.Holder)
	if err != nil {
		return domain.Grant{}, err
	}
	competence, err := domain.ParseCompetence(cmd.Competence)
	if err != nil {
		return domain.Grant{}, err
	}
	reign, err := domain.ParseReignVersion(cmd.Reign)
	if err != nil {
		return domain.Grant{}, err
	}
	current, err := uc.reigns.Current(ctx, season)
	if err != nil {
		return domain.Grant{}, err
	}
	return domain.AuthorizeAct(domain.Claim{
		Act: act, Season: season, Holder: holder,
		Competence: competence, Reign: reign, Operator: cmd.Operator,
	}, cmd.Session, cmd.Delegation, current, domain.AuthContext{
		Now: cmd.Now, MaxSessionAge: cmd.MaxSessionAge,
	})
}
