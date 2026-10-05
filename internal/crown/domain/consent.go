package domain

import (
	"strings"
	"time"
)

// AccountStatus represents the discrete lifecycle state of an account for royal office eligibility.
type AccountStatus string

const (
	// AccountStatusActive names an active account in good standing.
	AccountStatusActive AccountStatus = "active"
	// AccountStatusSuspended names an account under preventive or administrative suspension.
	AccountStatusSuspended AccountStatus = "suspended"
	// AccountStatusDead names a digitally or physically deceased account.
	AccountStatusDead AccountStatus = "dead"
)

// ParseAccountStatus validates one account status token.
func ParseAccountStatus(raw string) (AccountStatus, error) {
	switch AccountStatus(raw) {
	case AccountStatusActive, AccountStatusSuspended, AccountStatusDead:
		return AccountStatus(raw), nil
	default:
		return "", ErrInvalidAuthority
	}
}

// ConsentDecision represents an express decision over constitutional or office terms.
type ConsentDecision string

const (
	// ConsentDecisionAccepted records express verifiable acceptance.
	ConsentDecisionAccepted ConsentDecision = "accepted"
	// ConsentDecisionRefused records express refusal of terms.
	ConsentDecisionRefused ConsentDecision = "refused"
)

// ParseConsentDecision validates one consent decision token.
func ParseConsentDecision(raw string) (ConsentDecision, error) {
	switch ConsentDecision(raw) {
	case ConsentDecisionAccepted, ConsentDecisionRefused:
		return ConsentDecision(raw), nil
	default:
		return "", ErrInvalidAuthority
	}
}

// CharterConsent records one subject's verifiable consent over the season's constitutional Charter.
type CharterConsent struct {
	Subject   HolderSubject
	Charter   CharterVersion
	Decision  ConsentDecision
	Digest    string
	DecidedAt time.Time
}

// ValidShape checks that all charter consent fields are well-formed.
func (c CharterConsent) ValidShape() error {
	if _, err := ParseHolderSubject(string(c.Subject)); err != nil {
		return err
	}
	if _, err := ParseCharterVersion(string(c.Charter)); err != nil {
		return err
	}
	if _, err := ParseConsentDecision(string(c.Decision)); err != nil {
		return err
	}
	if !isDigest(c.Digest) {
		return ErrInvalidAuthority
	}
	if c.DecidedAt.IsZero() {
		return ErrInvalidAuthority
	}
	return nil
}

// OfficeTermsConsent records one subject's verifiable consent over season-specific royal office terms.
type OfficeTermsConsent struct {
	Subject      HolderSubject
	Season       SeasonID
	TermsVersion string
	Decision     ConsentDecision
	Digest       string
	DecidedAt    time.Time
}

// ValidShape checks that all office terms consent fields are well-formed.
func (c OfficeTermsConsent) ValidShape() error {
	if _, err := ParseHolderSubject(string(c.Subject)); err != nil {
		return err
	}
	if _, err := ParseSeasonID(string(c.Season)); err != nil {
		return err
	}
	if c.TermsVersion == "" || strings.TrimSpace(c.TermsVersion) != c.TermsVersion {
		return ErrInvalidAuthority
	}
	if _, err := ParseConsentDecision(string(c.Decision)); err != nil {
		return err
	}
	if !isDigest(c.Digest) {
		return ErrInvalidAuthority
	}
	if c.DecidedAt.IsZero() {
		return ErrInvalidAuthority
	}
	return nil
}

// MFAState records the multi-factor authentication configuration of a candidate.
type MFAState struct {
	Enabled    bool
	VerifiedAt time.Time
}

// Verified reports whether multi-factor authentication is enabled and verified.
func (m MFAState) Verified() bool {
	return m.Enabled && !m.VerifiedAt.IsZero()
}

// DisciplinaryRecord captures safety and disciplinary status.
//
// Invariants (docs/reino/TEMPORADAS_SUCESSAO.md §8):
// - Mere complaints (PendingComplaints) without formal conviction DO NOT impede eligibility.
// - Interested claims by rivals or adversaries (AdversaryClaims) DO NOT impede eligibility.
// - Only final formal convictions (FormalConvictions) create an impediment.
type DisciplinaryRecord struct {
	PendingComplaints []string
	AdversaryClaims   []string
	FormalConvictions []string
}

// HasFormalImpediment reports whether there is an adjudicated formal conviction.
func (d DisciplinaryRecord) HasFormalImpediment() bool {
	return len(d.FormalConvictions) > 0
}

// PendingRequirement names one unsatisfied prerequisite gating investiture.
type PendingRequirement string

const (
	// RequirementAccountNotActive indicates account is suspended or dead.
	RequirementAccountNotActive PendingRequirement = "account-not-active"
	// RequirementMFAMissing indicates multi-factor authentication is not verified.
	RequirementMFAMissing PendingRequirement = "mfa-missing"
	// RequirementCharterConsentMissing indicates Charter has not been verifiably accepted.
	RequirementCharterConsentMissing PendingRequirement = "charter-consent-missing"
	// RequirementOfficeTermsMissing indicates seasonal royal office terms have not been accepted.
	RequirementOfficeTermsMissing PendingRequirement = "office-terms-missing"
	// RequirementOfficeConsentRefused indicates candidate expressly refused office terms.
	RequirementOfficeConsentRefused PendingRequirement = "office-consent-refused"
	// RequirementFormalImpediment indicates a formal disqualifying conviction exists.
	RequirementFormalImpediment PendingRequirement = "formal-impediment"
	// RequirementAuthorityRegression indicates an authority version regression was detected.
	RequirementAuthorityRegression PendingRequirement = "authority-version-regression"
)

// PrivatePendingRequirementsDisclosure carries pending requirement status disclosed ONLY to the titular.
// It contains NO credentials, passwords, tokens, private keys or TOTP secrets.
type PrivatePendingRequirementsDisclosure struct {
	Subject              HolderSubject
	Season               SeasonID
	HasWealthSuperiority bool
	PendingRequirements  []PendingRequirement
	DisclosedAt          time.Time
}

// ForSubject returns the disclosure if and only if the requester is the titular account holder.
// Any third party or adversary query is refused with ErrPrivateDisclosureUnauthorized.
func (p PrivatePendingRequirementsDisclosure) ForSubject(requester HolderSubject) (PrivatePendingRequirementsDisclosure, error) {
	if requester == "" || requester != p.Subject {
		return PrivatePendingRequirementsDisclosure{}, ErrPrivateDisclosureUnauthorized
	}
	return p, nil
}

// SubjectConsentProfile contains all verifiable consent and identity data for a candidate.
type SubjectConsentProfile struct {
	Subject          HolderSubject
	AccountStatus    AccountStatus
	AuthorityVersion AuthorityVersion
	MFA              MFAState
	Charter          *CharterConsent
	OfficeTerms      *OfficeTermsConsent
	Disciplinary     DisciplinaryRecord
}

// EligibilityContext defines the current season and constitutional environment for eligibility evaluation.
type EligibilityContext struct {
	Season                   SeasonID
	Charter                  CharterVersion
	RequiredTermsVersion     string
	RequiredTermsDigest      string
	ExpectedCharterDigest    string
	RecordedAuthorityVersion AuthorityVersion
	HasWealthSuperiority     bool
	Now                      time.Time
}

// ConsentEligibilityResult captures the outcome of verifiable consent and eligibility evaluation.
type ConsentEligibilityResult struct {
	Subject              HolderSubject
	Eligible             bool
	PendingRequirements  []PendingRequirement
	Disclosure           PrivatePendingRequirementsDisclosure
	AuthorityVersion     AuthorityVersion
	BalancePreserved     bool
	ParticipationAllowed bool
}

// EvaluateConsentEligibility evaluates one candidate against verifiable consent criteria.
//
// Invariants (P47-T05 / docs/reino/TEMPORADAS_SUCESSAO.md §8):
// 1. Account state: dead or suspended accounts are ineligible.
// 2. MFA: without verified MFA, wealth does not confer authority.
// 3. Charter: must match season Charter version; tampered digest fails closed.
// 4. Terms: must match current season; terms from another season fail closed.
// 5. Altered terms: tampered digest fails closed with ErrTamperedConsent.
// 6. Refusal: express refusal keeps candidate's balance intact and permits game participation.
// 7. Non-impediment: mere complaint (denúncia sem decisão) or adversary claim does not impede.
// 8. Authority version: version lower than recorded refuses with ErrAuthorityVersionRegression.
// 9. No auto-acceptance: missing terms are never auto-accepted.
// 10. Privacy: pending requirements are disclosed only to the titular holder without secrets.
func validateEligibilityContext(profile SubjectConsentProfile, ctx EligibilityContext) error {
	if _, err := ParseSeasonID(string(ctx.Season)); err != nil {
		return err
	}
	if _, err := ParseCharterVersion(string(ctx.Charter)); err != nil {
		return err
	}
	if ctx.RequiredTermsVersion == "" {
		return ErrInvalidAuthority
	}
	if !isDigest(ctx.RequiredTermsDigest) {
		return ErrInvalidAuthority
	}
	if ctx.ExpectedCharterDigest != "" && !isDigest(ctx.ExpectedCharterDigest) {
		return ErrInvalidAuthority
	}
	if ctx.Now.IsZero() {
		return ErrInvalidAuthority
	}
	if _, err := ParseHolderSubject(string(profile.Subject)); err != nil {
		return err
	}
	if ctx.RecordedAuthorityVersion > 0 && profile.AuthorityVersion < ctx.RecordedAuthorityVersion {
		return ErrAuthorityVersionRegression
	}
	return nil
}

func checkAccountStatus(status AccountStatus) (*PendingRequirement, error) {
	switch status {
	case AccountStatusActive:
		return nil, nil
	case AccountStatusDead, AccountStatusSuspended:
		req := RequirementAccountNotActive
		return &req, nil
	default:
		return nil, ErrInvalidAuthority
	}
}

func checkCharterConsent(charter *CharterConsent, ctx EligibilityContext) (*PendingRequirement, error) {
	if charter == nil {
		req := RequirementCharterConsentMissing
		return &req, nil
	}
	if err := charter.ValidShape(); err != nil {
		return nil, err
	}
	if ctx.ExpectedCharterDigest != "" && charter.Digest != ctx.ExpectedCharterDigest {
		return nil, ErrTamperedConsent
	}
	if charter.Charter != ctx.Charter || charter.Decision != ConsentDecisionAccepted {
		req := RequirementCharterConsentMissing
		return &req, nil
	}
	return nil, nil
}

func checkOfficeTermsConsent(terms *OfficeTermsConsent, ctx EligibilityContext) (*PendingRequirement, error) {
	if terms == nil {
		req := RequirementOfficeTermsMissing
		return &req, nil
	}
	if err := terms.ValidShape(); err != nil {
		return nil, err
	}
	if terms.Season != ctx.Season {
		return nil, ErrTermsSeasonMismatch
	}
	if terms.Digest != ctx.RequiredTermsDigest {
		return nil, ErrTamperedConsent
	}
	if terms.TermsVersion != ctx.RequiredTermsVersion {
		req := RequirementOfficeTermsMissing
		return &req, nil
	}
	if terms.Decision == ConsentDecisionRefused {
		req := RequirementOfficeConsentRefused
		return &req, nil
	}
	if terms.Decision != ConsentDecisionAccepted {
		req := RequirementOfficeTermsMissing
		return &req, nil
	}
	return nil, nil
}

// EvaluateConsentEligibility evaluates one candidate against verifiable consent criteria.
//
// Invariants (P47-T05 / docs/reino/TEMPORADAS_SUCESSAO.md §8):
// 1. Account state: dead or suspended accounts are ineligible.
// 2. MFA: without verified MFA, wealth does not confer authority.
// 3. Charter: must match season Charter version; tampered digest fails closed.
// 4. Terms: must match current season; terms from another season fail closed.
// 5. Altered terms: tampered digest fails closed with ErrTamperedConsent.
// 6. Refusal: express refusal keeps candidate's balance intact and permits game participation.
// 7. Non-impediment: mere complaint (denúncia sem decisão) or adversary claim does not impede.
// 8. Authority version: version lower than recorded refuses with ErrAuthorityVersionRegression.
// 9. No auto-acceptance: missing terms are never auto-accepted.
// 10. Privacy: pending requirements are disclosed only to the titular holder without secrets.
func EvaluateConsentEligibility(
	profile SubjectConsentProfile,
	ctx EligibilityContext,
) (ConsentEligibilityResult, error) {
	if err := validateEligibilityContext(profile, ctx); err != nil {
		return ConsentEligibilityResult{}, err
	}

	var pending []PendingRequirement

	req, err := checkAccountStatus(profile.AccountStatus)
	if err != nil {
		return ConsentEligibilityResult{}, err
	}
	if req != nil {
		pending = append(pending, *req)
	}

	if !profile.MFA.Verified() {
		pending = append(pending, RequirementMFAMissing)
	}

	charterReq, err := checkCharterConsent(profile.Charter, ctx)
	if err != nil {
		return ConsentEligibilityResult{}, err
	}
	if charterReq != nil {
		pending = append(pending, *charterReq)
	}

	termsReq, err := checkOfficeTermsConsent(profile.OfficeTerms, ctx)
	if err != nil {
		return ConsentEligibilityResult{}, err
	}
	if termsReq != nil {
		pending = append(pending, *termsReq)
	}

	// Disciplinary: unadjudicated complaints and adversary claims DO NOT impede!
	if profile.Disciplinary.HasFormalImpediment() {
		pending = append(pending, RequirementFormalImpediment)
	}

	eligible := len(pending) == 0

	disclosure := PrivatePendingRequirementsDisclosure{
		Subject:              profile.Subject,
		Season:               ctx.Season,
		HasWealthSuperiority: ctx.HasWealthSuperiority,
		PendingRequirements:  pending,
		DisclosedAt:          ctx.Now.UTC(),
	}

	return ConsentEligibilityResult{
		Subject:              profile.Subject,
		Eligible:             eligible,
		PendingRequirements:  pending,
		Disclosure:           disclosure,
		AuthorityVersion:     profile.AuthorityVersion,
		BalancePreserved:     true, // Balance is always preserved (never confiscated)
		ParticipationAllowed: true, // Participation in game remains permitted
	}, nil
}

// AssertAccountEligible provides an explicit direct assertion for account status.
func AssertAccountEligible(status AccountStatus) error {
	switch status {
	case AccountStatusActive:
		return nil
	case AccountStatusDead:
		return ErrAccountDead
	case AccountStatusSuspended:
		return ErrAccountSuspended
	default:
		return ErrInvalidAuthority
	}
}

// AssertMFAEligible provides an explicit direct assertion for multi-factor authentication.
func AssertMFAEligible(mfa MFAState) error {
	if !mfa.Verified() {
		return ErrMFAMissing
	}
	return nil
}
