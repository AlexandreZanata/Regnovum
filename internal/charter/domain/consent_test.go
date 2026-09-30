package domain

import (
	"errors"
	"testing"
	"time"
)

const (
	consentHashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	consentHashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func consentGenesis() time.Time {
	return time.Date(2026, time.January, 4, 0, 0, 0, 0, time.UTC)
}

func consentChain(t *testing.T) []Release {
	t.Helper()
	genesis := consentGenesis()
	first, err := Publish(nil, PublishRequest{
		Version: "v1", Locale: "pt", EffectiveAt: genesis, ContentHash: consentHashA,
	})
	if err != nil {
		t.Fatalf("Publish v1: %v", err)
	}
	second, err := Publish([]Release{first}, PublishRequest{
		Version: "v2", Locale: "pt", EffectiveAt: genesis.AddDate(0, 0, 7),
		Previous: "v1", ContentHash: consentHashB,
	})
	if err != nil {
		t.Fatalf("Publish v2: %v", err)
	}
	return []Release{first, second}
}

func consentReq() ConsentRequest {
	return ConsentRequest{
		Account: "campones-1", Version: "v1", Locale: "pt",
		ShownHash: consentHashA, PriorRights: []string{"legacy-purchase-7"},
		DecidedAt: consentGenesis().Add(time.Hour), Decision: DecisionAccepted,
	}
}

func mustRecord(t *testing.T, chain []Release, ledger []Consent, req ConsentRequest) ([]Consent, Consent) {
	t.Helper()
	consent, err := RecordConsent(chain, ledger, req)
	if err != nil {
		t.Fatalf("RecordConsent(%+v): %v", req, err)
	}
	if err := consent.VerifyConsentHash(); err != nil {
		t.Fatalf("VerifyConsentHash: %v", err)
	}
	return append(ledger, consent), consent
}

func TestAcceptanceBindsDisplayToDecision(t *testing.T) {
	chain := consentChain(t)
	_, accepted := mustRecord(t, chain, nil, consentReq())
	if accepted.Decision != DecisionAccepted || accepted.ShownHash != consentHashA {
		t.Fatalf("consent = %+v, want accepted with the shown digest", accepted)
	}
	swapped := consentReq()
	swapped.ShownHash = consentHashB
	if _, err := RecordConsent(chain, nil, swapped); !errors.Is(err, ErrInvalidCharter) {
		t.Fatalf("swapped display = %v, want ErrInvalidCharter: the shown version seals the verdict", err)
	}
	forged := consentReq()
	forged.Decision = ""
	if _, err := RecordConsent(chain, nil, forged); !errors.Is(err, ErrInvalidCharter) {
		t.Fatalf("blank decision = %v, want ErrInvalidCharter: no implicit acceptance", err)
	}
	if _, err := RecordConsent(chain, nil, ConsentRequest{
		Account: "campones-1", Version: "v9", Locale: "pt",
		ShownHash: consentHashA, DecidedAt: consentGenesis().Add(time.Hour), Decision: DecisionAccepted,
	}); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("missing version = %v, want ErrUnknownVersion blocking new acceptance", err)
	}
}

func TestForgedAcceptancesBreakOnTheSeal(t *testing.T) {
	chain := consentChain(t)
	_, accepted := mustRecord(t, chain, nil, consentReq())
	forged := accepted
	forged.Decision = DecisionRefused
	if err := forged.VerifyConsentHash(); !errors.Is(err, ErrInvalidCharter) {
		t.Fatalf("forged verdict Verify = %v, want ErrInvalidCharter", err)
	}
	if _, err := RecordConsent(chain, []Consent{forged}, consentReq()); !errors.Is(err, ErrInvalidCharter) {
		t.Fatalf("tampered ledger = %v, want ErrInvalidCharter before any replay", err)
	}
}

func TestRefusalLimitsWithoutRemovingPaidRights(t *testing.T) {
	chain := consentChain(t)
	req := consentReq()
	req.Decision = DecisionRefused
	_, refused := mustRecord(t, chain, nil, req)
	if len(refused.PriorRights) != 1 || refused.PriorRights[0] != "legacy-purchase-7" {
		t.Fatalf("refusal prior rights = %v, want the legacy purchase intact", refused.PriorRights)
	}
	rights := RightsOf(refused.Decision)
	for _, want := range []PreservedRight{RightHistory, RightExport, RightRecourse, RightSettlement} {
		found := false
		for _, got := range rights {
			found = found || got == want
		}
		if !found {
			t.Fatalf("refusal rights = %v, want %q preserved", rights, want)
		}
	}
	for _, got := range rights {
		if got == RightNewActivities {
			t.Fatalf("refusal rights = %v, want new activities limited", rights)
		}
	}
	if len(RightsOf(DecisionAccepted)) != 5 {
		t.Fatal("acceptance must unlock exactly the four preserved rights plus new activities")
	}
}

func TestOneVerdictPerAccountAndVersion(t *testing.T) {
	chain := consentChain(t)
	ledger, first := mustRecord(t, chain, nil, consentReq())
	replay, err := RecordConsent(chain, ledger, consentReq())
	if err != nil || replay.Hash != first.Hash {
		t.Fatalf("replay = %+v/%v, want the identical verdict idempotently", replay, err)
	}
	divergent := consentReq()
	divergent.Decision = DecisionRefused
	if _, err := RecordConsent(chain, ledger, divergent); !errors.Is(err, ErrConsentConflict) {
		t.Fatalf("divergent re-verdict = %v, want ErrConsentConflict", err)
	}
	mind, err := RecordConsent(chain, ledger, ConsentRequest{
		Account: "campones-1", Version: "v2", Locale: "pt",
		ShownHash: consentHashB, PriorRights: []string{"legacy-purchase-7"},
		DecidedAt: consentGenesis().AddDate(0, 0, 8), Decision: DecisionAccepted,
	})
	if err != nil || mind.Version != "v2" {
		t.Fatalf("change of mind = %+v/%v, want acceptance traveling via v2", mind, err)
	}
}
