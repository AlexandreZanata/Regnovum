package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func validTestDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func baseTestProfile(subject string, now time.Time) SubjectConsentProfile {
	charterDigest := validTestDigest("charter-v1-content")
	termsDigest := validTestDigest("office-terms-v1-season-1")

	return SubjectConsentProfile{
		Subject:          HolderSubject(subject),
		AccountStatus:    AccountStatusActive,
		AuthorityVersion: AuthorityVersion(1),
		MFA: MFAState{
			Enabled:    true,
			VerifiedAt: now.Add(-10 * time.Minute),
		},
		Charter: &CharterConsent{
			Subject:   HolderSubject(subject),
			Charter:   CharterVersion("v1"),
			Decision:  ConsentDecisionAccepted,
			Digest:    charterDigest,
			DecidedAt: now.Add(-20 * time.Minute),
		},
		OfficeTerms: &OfficeTermsConsent{
			Subject:      HolderSubject(subject),
			Season:       SeasonID("season-1"),
			TermsVersion: "v1",
			Decision:     ConsentDecisionAccepted,
			Digest:       termsDigest,
			DecidedAt:    now.Add(-5 * time.Minute),
		},
		Disciplinary: DisciplinaryRecord{
			PendingComplaints: nil,
			AdversaryClaims:   nil,
			FormalConvictions: nil,
		},
	}
}

func baseTestContext(now time.Time) EligibilityContext {
	return EligibilityContext{
		Season:                   SeasonID("season-1"),
		Charter:                  CharterVersion("v1"),
		RequiredTermsVersion:     "v1",
		RequiredTermsDigest:      validTestDigest("office-terms-v1-season-1"),
		ExpectedCharterDigest:    validTestDigest("charter-v1-content"),
		RecordedAuthorityVersion: AuthorityVersion(1),
		HasWealthSuperiority:     true,
		Now:                      now,
	}
}

func TestConsentEligibilityHappyPath(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	profile := baseTestProfile("ana", now)
	ctx := baseTestContext(now)

	res, err := EvaluateConsentEligibility(profile, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Eligible {
		t.Fatalf("expected candidate to be eligible, got pending: %v", res.PendingRequirements)
	}
	if len(res.PendingRequirements) != 0 {
		t.Errorf("expected 0 pending requirements, got: %v", res.PendingRequirements)
	}
	if !res.BalancePreserved {
		t.Errorf("expected balance to be preserved")
	}
	if !res.ParticipationAllowed {
		t.Errorf("expected participation to be allowed")
	}
}

func TestConsentAccountDeadAndSuspended(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	t.Run("dead account fails closed and is ineligible", func(t *testing.T) {
		profile := baseTestProfile("carlos", now)
		profile.AccountStatus = AccountStatusDead
		ctx := baseTestContext(now)

		res, err := EvaluateConsentEligibility(profile, ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Eligible {
			t.Fatalf("dead account must be ineligible")
		}
		found := false
		for _, r := range res.PendingRequirements {
			if r == RequirementAccountNotActive {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected RequirementAccountNotActive in pending requirements, got %v", res.PendingRequirements)
		}
		if err := AssertAccountEligible(profile.AccountStatus); !errors.Is(err, ErrAccountDead) {
			t.Errorf("AssertAccountEligible(AccountStatusDead) = %v, want ErrAccountDead", err)
		}
	})

	t.Run("suspended account fails closed and is ineligible", func(t *testing.T) {
		profile := baseTestProfile("diana", now)
		profile.AccountStatus = AccountStatusSuspended
		ctx := baseTestContext(now)

		res, err := EvaluateConsentEligibility(profile, ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Eligible {
			t.Fatalf("suspended account must be ineligible")
		}
		if err := AssertAccountEligible(profile.AccountStatus); !errors.Is(err, ErrAccountSuspended) {
			t.Errorf("AssertAccountEligible(AccountStatusSuspended) = %v, want ErrAccountSuspended", err)
		}
	})
}

func TestConsentMFAMissing(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	t.Run("disabled MFA denies credentials and flags pending requirement", func(t *testing.T) {
		profile := baseTestProfile("eduardo", now)
		profile.MFA = MFAState{Enabled: false, VerifiedAt: time.Time{}}
		ctx := baseTestContext(now)

		res, err := EvaluateConsentEligibility(profile, ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Eligible {
			t.Fatalf("candidate without MFA must NOT be eligible")
		}
		found := false
		for _, r := range res.PendingRequirements {
			if r == RequirementMFAMissing {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected RequirementMFAMissing, got %v", res.PendingRequirements)
		}
		if err := AssertMFAEligible(profile.MFA); !errors.Is(err, ErrMFAMissing) {
			t.Errorf("AssertMFAEligible = %v, want ErrMFAMissing", err)
		}
	})

	t.Run("enabled MFA with zero verified instant is ineligible", func(t *testing.T) {
		profile := baseTestProfile("felipe", now)
		profile.MFA = MFAState{Enabled: true, VerifiedAt: time.Time{}}
		ctx := baseTestContext(now)

		res, err := EvaluateConsentEligibility(profile, ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Eligible {
			t.Fatalf("candidate without verified instant must NOT be eligible")
		}
	})
}

func TestConsentTermsOfAnotherSeasonRefused(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	profile := baseTestProfile("gabriel", now)
	// Terms accepted for season-previous, offered in season-1:
	profile.OfficeTerms.Season = SeasonID("season-previous")

	ctx := baseTestContext(now)
	ctx.Season = SeasonID("season-1")

	_, err := EvaluateConsentEligibility(profile, ctx)
	if !errors.Is(err, ErrTermsSeasonMismatch) {
		t.Fatalf("EvaluateConsentEligibility with terms of another season = %v, want ErrTermsSeasonMismatch", err)
	}
}

func TestConsentTamperedDigest(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	t.Run("tampered office terms digest refuses with ErrTamperedConsent", func(t *testing.T) {
		profile := baseTestProfile("helen", now)
		profile.OfficeTerms.Digest = validTestDigest("altered-office-terms-payload")
		ctx := baseTestContext(now)

		_, err := EvaluateConsentEligibility(profile, ctx)
		if !errors.Is(err, ErrTamperedConsent) {
			t.Fatalf("expected ErrTamperedConsent, got: %v", err)
		}
	})

	t.Run("tampered charter digest refuses with ErrTamperedConsent", func(t *testing.T) {
		profile := baseTestProfile("igor", now)
		profile.Charter.Digest = validTestDigest("altered-charter-payload")
		ctx := baseTestContext(now)

		_, err := EvaluateConsentEligibility(profile, ctx)
		if !errors.Is(err, ErrTamperedConsent) {
			t.Fatalf("expected ErrTamperedConsent, got: %v", err)
		}
	})
}

func TestConsentUnadjudicatedComplaintAndAdversaryClaimDoNotImpede(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	t.Run("denúncia sem decisão never impedes candidate", func(t *testing.T) {
		profile := baseTestProfile("julia", now)
		// Active complaint pending investigation, no conviction:
		profile.Disciplinary.PendingComplaints = []string{"denuncia-seguranca-001", "denuncia-conduta-002"}
		ctx := baseTestContext(now)

		res, err := EvaluateConsentEligibility(profile, ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.Eligible {
			t.Fatalf("candidate with unadjudicated complaints MUST remain eligible, got pending: %v", res.PendingRequirements)
		}
	})

	t.Run("adversary interested claim never impedes candidate", func(t *testing.T) {
		profile := baseTestProfile("kleber", now)
		// Rival claimant filed an interested objection:
		profile.Disciplinary.AdversaryClaims = []string{"objecao-rival-bob"}
		ctx := baseTestContext(now)

		res, err := EvaluateConsentEligibility(profile, ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.Eligible {
			t.Fatalf("candidate with adversary claim MUST remain eligible, got pending: %v", res.PendingRequirements)
		}
	})

	t.Run("formal conviction DOES impede candidate", func(t *testing.T) {
		profile := baseTestProfile("lucas", now)
		profile.Disciplinary.FormalConvictions = []string{"condenacao-inquisicao-final"}
		ctx := baseTestContext(now)

		res, err := EvaluateConsentEligibility(profile, ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Eligible {
			t.Fatalf("candidate with formal conviction MUST be ineligible")
		}
		found := false
		for _, r := range res.PendingRequirements {
			if r == RequirementFormalImpediment {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected RequirementFormalImpediment, got %v", res.PendingRequirements)
		}
	})
}

func TestConsentAuthorityVersionRegression(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	t.Run("evaluation with regressed authority version refuses with ErrAuthorityVersionRegression", func(t *testing.T) {
		profile := baseTestProfile("marcos", now)
		profile.AuthorityVersion = AuthorityVersion(2)

		ctx := baseTestContext(now)
		ctx.RecordedAuthorityVersion = AuthorityVersion(3) // Current recorded version is higher!

		_, err := EvaluateConsentEligibility(profile, ctx)
		if !errors.Is(err, ErrAuthorityVersionRegression) {
			t.Fatalf("expected ErrAuthorityVersionRegression, got %v", err)
		}
	})

	t.Run("VerifyAuthorityEffect refuses regressed version at effect time", func(t *testing.T) {
		reign := CurrentReign{
			Season:           SeasonID("season-1"),
			Holder:           HolderSubject("marcos"),
			Reign:            ReignVersion(1),
			AuthorityVersion: AuthorityVersion(3),
			StartsAt:         now.Add(-time.Hour),
			EndsAt:           now.Add(time.Hour),
			Open:             true,
		}

		// Presented version is 2, recorded is 3 -> regression!
		err := VerifyAuthorityEffect(reign, AuthorityVersion(3), AuthorityVersion(2))
		if !errors.Is(err, ErrAuthorityVersionRegression) {
			t.Errorf("VerifyAuthorityEffect with regressed version = %v, want ErrAuthorityVersionRegression", err)
		}

		// Presented version is 3, recorded is 3 -> ok
		if err := VerifyAuthorityEffect(reign, AuthorityVersion(3), AuthorityVersion(3)); err != nil {
			t.Errorf("VerifyAuthorityEffect with current version = %v, want nil", err)
		}
	})
}

func TestConsentOfficeRefusalPreservesBalanceAndParticipation(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	profile := baseTestProfile("nelson", now)
	// Candidate explicitly refused royal office terms:
	profile.OfficeTerms.Decision = ConsentDecisionRefused

	ctx := baseTestContext(now)

	res, err := EvaluateConsentEligibility(profile, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Eligible {
		t.Fatalf("candidate who refused office terms must NOT be eligible to receive throne")
	}
	found := false
	for _, r := range res.PendingRequirements {
		if r == RequirementOfficeConsentRefused {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected RequirementOfficeConsentRefused, got %v", res.PendingRequirements)
	}
	// Invariants: recusa mantém saldo e participação permitida!
	if !res.BalancePreserved {
		t.Errorf("refusal of office MUST preserve candidate balance")
	}
	if !res.ParticipationAllowed {
		t.Errorf("refusal of office MUST keep game participation allowed")
	}
}

func TestConsentNoAutoAcceptance(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	profile := baseTestProfile("olivia", now)
	// System must NEVER auto-accept: unaccepted/nil terms remain unaccepted
	profile.OfficeTerms = nil

	ctx := baseTestContext(now)

	res, err := EvaluateConsentEligibility(profile, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Eligible {
		t.Fatalf("candidate without terms acceptance must NOT be eligible")
	}
	found := false
	for _, r := range res.PendingRequirements {
		if r == RequirementOfficeTermsMissing {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected RequirementOfficeTermsMissing, got %v", res.PendingRequirements)
	}
}

func TestPrivateDisclosurePrivacyAndNoSecrets(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	profile := baseTestProfile("paula", now)
	profile.MFA = MFAState{Enabled: false} // Pending requirement

	ctx := baseTestContext(now)

	res, err := EvaluateConsentEligibility(profile, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("titular account holder can access own pending requirements", func(t *testing.T) {
		disc, err := res.Disclosure.ForSubject(HolderSubject("paula"))
		if err != nil {
			t.Fatalf("titular access failed: %v", err)
		}
		if disc.Subject != HolderSubject("paula") {
			t.Errorf("subject mismatch: %s", disc.Subject)
		}
		if len(disc.PendingRequirements) == 0 {
			t.Errorf("expected pending requirements, got 0")
		}
	})

	t.Run("rival or third party cannot access pending requirements", func(t *testing.T) {
		_, err := res.Disclosure.ForSubject(HolderSubject("adversary-bob"))
		if !errors.Is(err, ErrPrivateDisclosureUnauthorized) {
			t.Errorf("third party access = %v, want ErrPrivateDisclosureUnauthorized", err)
		}
	})

	t.Run("disclosure carries zero secrets or credentials", func(t *testing.T) {
		disc, err := res.Disclosure.ForSubject(HolderSubject("paula"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Inspect all fields to ensure no secrets/tokens/keys exist
		for _, req := range disc.PendingRequirements {
			str := string(req)
			if strings.Contains(strings.ToLower(str), "password") ||
				strings.Contains(strings.ToLower(str), "token") ||
				strings.Contains(strings.ToLower(str), "secret") ||
				strings.Contains(strings.ToLower(str), "key") {
				t.Errorf("leak detected in requirement string: %s", str)
			}
		}
	})
}
