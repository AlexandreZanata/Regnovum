package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

type fakeConsentSource struct {
	profile domain.SubjectConsentProfile
	err     error
}

func (f *fakeConsentSource) LoadConsentProfile(
	_ context.Context,
	_ domain.SeasonID,
	_ domain.HolderSubject,
) (domain.SubjectConsentProfile, error) {
	if f.err != nil {
		return domain.SubjectConsentProfile{}, f.err
	}
	return f.profile, nil
}

func (f *fakeConsentSource) RecordOfficeTermsConsent(
	_ context.Context,
	_ domain.OfficeTermsConsent,
) error {
	return f.err
}

func validAppDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func makeAppProfile(subject string, now time.Time) domain.SubjectConsentProfile {
	return domain.SubjectConsentProfile{
		Subject:          domain.HolderSubject(subject),
		AccountStatus:    domain.AccountStatusActive,
		AuthorityVersion: domain.AuthorityVersion(1),
		MFA: domain.MFAState{
			Enabled:    true,
			VerifiedAt: now.Add(-10 * time.Minute),
		},
		Charter: &domain.CharterConsent{
			Subject:   domain.HolderSubject(subject),
			Charter:   domain.CharterVersion("v1"),
			Decision:  domain.ConsentDecisionAccepted,
			Digest:    validAppDigest("charter-v1"),
			DecidedAt: now.Add(-20 * time.Minute),
		},
		OfficeTerms: &domain.OfficeTermsConsent{
			Subject:      domain.HolderSubject(subject),
			Season:       domain.SeasonID("season-1"),
			TermsVersion: "v1",
			Decision:     domain.ConsentDecisionAccepted,
			Digest:       validAppDigest("terms-v1-season-1"),
			DecidedAt:    now.Add(-5 * time.Minute),
		},
	}
}

func TestEligibilityUseCaseGrantsViaPort(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	fake := &fakeConsentSource{
		profile: makeAppProfile("ana", now),
	}
	uc := NewEligibilityUseCase(fake)

	cmd := EvaluateEligibilityCommand{
		Season:                   "season-1",
		Subject:                  "ana",
		Charter:                  "v1",
		TermsVersion:             "v1",
		TermsDigest:              validAppDigest("terms-v1-season-1"),
		ExpectedCharterDigest:    validAppDigest("charter-v1"),
		RecordedAuthorityVersion: 1,
		HasWealthSuperiority:     true,
		Now:                      now,
	}

	res, err := uc.Evaluate(context.Background(), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Eligible {
		t.Fatalf("expected candidate to be eligible, got pending: %v", res.PendingRequirements)
	}
}

func TestEligibilityUseCaseFailsClosedWhenDependencyUnavailable(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	// Simulate database or remote identity provider outage:
	fake := &fakeConsentSource{
		err: fmt.Errorf("connection refused to identity store"),
	}
	uc := NewEligibilityUseCase(fake)

	cmd := EvaluateEligibilityCommand{
		Season:                   "season-1",
		Subject:                  "bob",
		Charter:                  "v1",
		TermsVersion:             "v1",
		TermsDigest:              validAppDigest("terms-v1-season-1"),
		ExpectedCharterDigest:    validAppDigest("charter-v1"),
		RecordedAuthorityVersion: 1,
		HasWealthSuperiority:     true,
		Now:                      now,
	}

	_, err := uc.Evaluate(context.Background(), cmd)
	if !errors.Is(err, domain.ErrIdentityDependencyUnavailable) {
		t.Fatalf("expected ErrIdentityDependencyUnavailable, got: %v", err)
	}
}

func TestEligibilityUseCaseBrowserAttributesCannotGrantCrown(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	// Server record has missing MFA:
	profile := makeAppProfile("hacker-claimant", now)
	profile.MFA = domain.MFAState{Enabled: false}

	fake := &fakeConsentSource{profile: profile}
	uc := NewEligibilityUseCase(fake)

	// Untrusted client sends fraudulent browser claims:
	cmd := EvaluateEligibilityCommand{
		Season:                   "season-1",
		Subject:                  "hacker-claimant",
		Charter:                  "v1",
		TermsVersion:             "v1",
		TermsDigest:              validAppDigest("terms-v1-season-1"),
		ExpectedCharterDigest:    validAppDigest("charter-v1"),
		RecordedAuthorityVersion: 1,
		HasWealthSuperiority:     true,
		Now:                      now,
		// Fraudulent claims from client:
		ClientSuppliedEligible: true,
		ClientSuppliedMFA:      true,
		ClientSuppliedCrown:    true,
	}

	res, err := uc.Evaluate(context.Background(), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Browser claims MUST NOT grant crown:
	if res.Eligible {
		t.Fatalf("browser-supplied attributes must NEVER grant crown!")
	}
	found := false
	for _, r := range res.PendingRequirements {
		if r == domain.RequirementMFAMissing {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected RequirementMFAMissing, got: %v", res.PendingRequirements)
	}
}

func TestEligibilityUseCaseDisclosePendingRequirementsPrivacy(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	profile := makeAppProfile("clara", now)
	profile.OfficeTerms = nil // missing terms

	fake := &fakeConsentSource{profile: profile}
	uc := NewEligibilityUseCase(fake)

	baseCmd := DisclosePendingRequirementsCommand{
		Season:                   "season-1",
		Subject:                  "clara",
		Charter:                  "v1",
		TermsVersion:             "v1",
		TermsDigest:              validAppDigest("terms-v1-season-1"),
		ExpectedCharterDigest:    validAppDigest("charter-v1"),
		RecordedAuthorityVersion: 1,
		HasWealthSuperiority:     true,
		Now:                      now,
	}

	t.Run("titular reads own pending requirements", func(t *testing.T) {
		cmd := baseCmd
		cmd.Requester = "clara"

		disc, err := uc.DisclosePendingRequirements(context.Background(), cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if disc.Subject != domain.HolderSubject("clara") {
			t.Errorf("subject mismatch: %s", disc.Subject)
		}
		if len(disc.PendingRequirements) == 0 {
			t.Errorf("expected pending requirements, got 0")
		}
	})

	t.Run("rival is denied access with ErrPrivateDisclosureUnauthorized", func(t *testing.T) {
		cmd := baseCmd
		cmd.Requester = "rival-danilo"

		_, err := uc.DisclosePendingRequirements(context.Background(), cmd)
		if !errors.Is(err, domain.ErrPrivateDisclosureUnauthorized) {
			t.Errorf("expected ErrPrivateDisclosureUnauthorized, got: %v", err)
		}
	})
}
