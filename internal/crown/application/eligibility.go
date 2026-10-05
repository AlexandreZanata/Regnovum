package application

import (
	"context"
	"fmt"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

// VerifiableConsentSource is the consumer port for candidate identity,
// consent and disciplinary state. Implementations query the identity,
// charter, season terms and disciplinary storage.
type VerifiableConsentSource interface {
	// LoadConsentProfile loads the candidate's verified identity, MFA, charter,
	// office terms and disciplinary record from authoritative storage.
	// If the provider fails, returns an error that causes evaluation to fail closed.
	LoadConsentProfile(
		ctx context.Context,
		season domain.SeasonID,
		subject domain.HolderSubject,
	) (domain.SubjectConsentProfile, error)

	// RecordOfficeTermsConsent stores an explicit, verifiable acceptance or refusal
	// of seasonal royal office terms.
	RecordOfficeTermsConsent(
		ctx context.Context,
		consent domain.OfficeTermsConsent,
	) error
}

// EvaluateEligibilityCommand carries the context and candidate identity
// for sovereign eligibility evaluation.
//
// Invariants (P47-T05):
//   - Client-supplied attributes (ClientSuppliedEligible, ClientSuppliedMFA, etc.)
//     are NEVER used to grant authority. All decisions rely strictly on
//     server-side verifiable records loaded through the port.
type EvaluateEligibilityCommand struct {
	Season                   string
	Subject                  string
	Charter                  string
	TermsVersion             string
	TermsDigest              string
	ExpectedCharterDigest    string
	RecordedAuthorityVersion int64
	HasWealthSuperiority     bool
	Now                      time.Time

	// Untrusted client-supplied attributes that MUST be ignored by the engine:
	ClientSuppliedEligible bool
	ClientSuppliedMFA      bool
	ClientSuppliedCrown    bool
}

// DisclosePendingRequirementsCommand carries a request to read pending requirements.
type DisclosePendingRequirementsCommand struct {
	Season                   string
	Subject                  string
	Requester                string
	Charter                  string
	TermsVersion             string
	TermsDigest              string
	ExpectedCharterDigest    string
	RecordedAuthorityVersion int64
	HasWealthSuperiority     bool
	Now                      time.Time
}

// EligibilityUseCase evaluates sovereign eligibility and verifiable consent.
type EligibilityUseCase struct {
	consents VerifiableConsentSource
}

// NewEligibilityUseCase creates an instance of EligibilityUseCase.
func NewEligibilityUseCase(consents VerifiableConsentSource) *EligibilityUseCase {
	return &EligibilityUseCase{consents: consents}
}

// Evaluate evaluates a candidate against verifiable consent criteria.
// If the identity or consent dependency is unavailable, it fails closed
// with domain.ErrIdentityDependencyUnavailable.
func (uc *EligibilityUseCase) Evaluate(
	ctx context.Context,
	cmd EvaluateEligibilityCommand,
) (domain.ConsentEligibilityResult, error) {
	season, err := domain.ParseSeasonID(cmd.Season)
	if err != nil {
		return domain.ConsentEligibilityResult{}, err
	}
	subject, err := domain.ParseHolderSubject(cmd.Subject)
	if err != nil {
		return domain.ConsentEligibilityResult{}, err
	}
	charter, err := domain.ParseCharterVersion(cmd.Charter)
	if err != nil {
		return domain.ConsentEligibilityResult{}, err
	}
	if cmd.Now.IsZero() {
		return domain.ConsentEligibilityResult{}, domain.ErrInvalidAuthority
	}

	profile, err := uc.consents.LoadConsentProfile(ctx, season, subject)
	if err != nil {
		// Dependency failure: identity or consent provider unavailable -> fail closed
		return domain.ConsentEligibilityResult{}, fmt.Errorf("%w: %v", domain.ErrIdentityDependencyUnavailable, err)
	}

	evalCtx := domain.EligibilityContext{
		Season:                   season,
		Charter:                  charter,
		RequiredTermsVersion:     cmd.TermsVersion,
		RequiredTermsDigest:      cmd.TermsDigest,
		ExpectedCharterDigest:    cmd.ExpectedCharterDigest,
		RecordedAuthorityVersion: domain.AuthorityVersion(cmd.RecordedAuthorityVersion),
		HasWealthSuperiority:     cmd.HasWealthSuperiority,
		Now:                      cmd.Now,
	}

	// Browser attributes are ignored; evaluate strictly against server-side profile
	return domain.EvaluateConsentEligibility(profile, evalCtx)
}

// DisclosePendingRequirements returns pending requirements for a candidate.
// It enforces privacy: disclosure is authorized ONLY for the titular account holder.
func (uc *EligibilityUseCase) DisclosePendingRequirements(
	ctx context.Context,
	cmd DisclosePendingRequirementsCommand,
) (domain.PrivatePendingRequirementsDisclosure, error) {
	evalCmd := EvaluateEligibilityCommand{
		Season:                   cmd.Season,
		Subject:                  cmd.Subject,
		Charter:                  cmd.Charter,
		TermsVersion:             cmd.TermsVersion,
		TermsDigest:              cmd.TermsDigest,
		ExpectedCharterDigest:    cmd.ExpectedCharterDigest,
		RecordedAuthorityVersion: cmd.RecordedAuthorityVersion,
		HasWealthSuperiority:     cmd.HasWealthSuperiority,
		Now:                      cmd.Now,
	}

	res, err := uc.Evaluate(ctx, evalCmd)
	if err != nil {
		return domain.PrivatePendingRequirementsDisclosure{}, err
	}

	requester, err := domain.ParseHolderSubject(cmd.Requester)
	if err != nil {
		return domain.PrivatePendingRequirementsDisclosure{}, err
	}

	return res.Disclosure.ForSubject(requester)
}
