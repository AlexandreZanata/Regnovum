package application

import (
	"context"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// ConsentCommand records one charter verdict for one account: the
// charter version cited and the express decision.
type ConsentCommand struct {
	AccountID string
	Charter   string
	Decision  string
}

// ConsentView is the recorded verdict both later reads resolve.
type ConsentView struct {
	AccountID string
	Charter   domain.CharterVersion
	Decision  domain.ConsentDecision
	DecidedAt time.Time
}

// OptInCommand requests the economic opt-in: account, charter version,
// quantity, conversion rate pair and validity bound. Recording the
// intent moves no value; conversion arrives in a later task and matches
// these terms verbatim.
type OptInCommand struct {
	AccountID string
	Charter   string
	Millis    int64
	RateNum   int64
	RateDen   int64
	ValidDays int
}

// OptInView is the recorded intent both later reads resolve.
type OptInView struct {
	OptInID   string
	AccountID string
	Charter   domain.CharterVersion
	Quantity  domain.MilliInk
	Rate      domain.ConversionRate
	ValidTill time.Time
}

// ConsentRepository persists charter verdicts and opt-in intents. New
// verdicts never rewrite old ones: one account holds at most one
// decision and one intent per charter version.
type ConsentRepository interface {
	// RecordConsent stores the verdict, replaying an identical one
	// untouched and refusing a divergent one.
	RecordConsent(ctx context.Context, accountID string, charter domain.CharterVersion, decision domain.ConsentDecision) (*ConsentView, error)
	// FindConsent resolves one account's verdict for one version, or
	// reports its absence. Callers only ever see their own account.
	FindConsent(ctx context.Context, accountID string, charter domain.CharterVersion) (*ConsentView, error)
	// RecordOptIn stores the intent after proving an acceptance stands
	// for the same account and version. Identical intents replay;
	// divergent terms conflict; intents without acceptance are refused.
	RecordOptIn(ctx context.Context, accountID string, charter domain.CharterVersion, quantity domain.MilliInk, rate domain.ConversionRate, validTill time.Time) (*OptInView, error)
}

// ConsentUseCase records one charter verdict. Refusal is a first-class
// outcome that preserves history, export, recourse and settlement by
// construction: it writes nothing anywhere else. It is an internal
// operation: no public surface calls it.
type ConsentUseCase struct {
	consents ConsentRepository
}

// NewConsentUseCase creates an instance of ConsentUseCase.
func NewConsentUseCase(consents ConsentRepository) *ConsentUseCase {
	return &ConsentUseCase{consents: consents}
}

// Execute validates and records the verdict.
func (uc *ConsentUseCase) Execute(ctx context.Context, cmd ConsentCommand) (*ConsentView, error) {
	if cmd.AccountID == "" {
		return nil, domain.ErrInvalidCharter
	}
	charter, err := domain.ParseCharterVersion(cmd.Charter)
	if err != nil {
		return nil, err
	}
	decision, err := domain.ParseConsentDecision(cmd.Decision)
	if err != nil {
		return nil, err
	}
	return uc.consents.RecordConsent(ctx, cmd.AccountID, charter, decision)
}

// OptInUseCase records one explicit economic opt-in intent. Without a
// prior acceptance for the same account and version there is no
// conversion to talk about, so the intent is refused up front. It is an
// internal operation: no public surface calls it.
type OptInUseCase struct {
	consents ConsentRepository
	clock    Clock
}

// NewOptInUseCase creates an instance of OptInUseCase.
func NewOptInUseCase(consents ConsentRepository, clock Clock) *OptInUseCase {
	return &OptInUseCase{consents: consents, clock: clock}
}

// Execute validates the intent terms and records them without moving value.
func (uc *OptInUseCase) Execute(ctx context.Context, cmd OptInCommand) (*OptInView, error) {
	if cmd.AccountID == "" {
		return nil, domain.ErrInvalidCharter
	}
	charter, err := domain.ParseCharterVersion(cmd.Charter)
	if err != nil {
		return nil, err
	}
	quantity, err := domain.NewMilliInk(cmd.Millis)
	if err != nil {
		return nil, err
	}
	if quantity.IsZero() {
		return nil, domain.ErrInvalidCharter
	}
	rate, err := domain.ParseConversionRate(cmd.RateNum, cmd.RateDen)
	if err != nil {
		return nil, err
	}
	if cmd.ValidDays <= 0 {
		return nil, domain.ErrInvalidCharter
	}
	consent, err := uc.consents.FindConsent(ctx, cmd.AccountID, charter)
	if err != nil {
		return nil, err
	}
	if consent == nil || consent.Decision != domain.ConsentAccepted {
		return nil, domain.ErrConsentRequired
	}
	return uc.consents.RecordOptIn(ctx, cmd.AccountID, charter, quantity, rate, uc.clock.Now().AddDate(0, 0, cmd.ValidDays).UTC())
}
