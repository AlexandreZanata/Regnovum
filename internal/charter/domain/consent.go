package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// maxConsentRunes bounds opaque consent tokens: account labels and
// prior-right identifiers. Long enough for operation tokens, short
// enough to stay out of log and index abuse.
const maxConsentRunes = 128

// Decision is the closed verdict vocabulary of one account over one
// charter version: express acceptance unlocks new activities under
// that version, express refusal limits the account to preserved
// rights. There is no implicit verdict: the zero value refuses.
type Decision string

const (
	// DecisionAccepted records an express acceptance.
	DecisionAccepted Decision = "accepted"
	// DecisionRefused records an express refusal.
	DecisionRefused Decision = "refused"
)

// ParseDecision validates a verdict token. Matching is exact: no
// trimming, no case folding, no default.
func ParseDecision(raw string) (Decision, error) {
	decision := Decision(raw)
	switch decision {
	case DecisionAccepted, DecisionRefused:
		return decision, nil
	default:
		return "", ErrInvalidCharter
	}
}

// String returns the stored verdict value.
func (d Decision) String() string { return string(d) }

// PreservedRight names what one account keeps, derived from the
// verdict itself rather than stored. The four travel with both
// verdicts — refusal changes nothing about earlier rights — and
// acceptance adds new activities without touching them either. The
// vocabulary mirrors the conversion consent rights without importing
// them: domains stay disjoint by architecture.
type PreservedRight string

const (
	// RightHistory keeps the readable past.
	RightHistory PreservedRight = "history"
	// RightExport keeps data portability.
	RightExport PreservedRight = "export"
	// RightRecourse keeps contestation.
	RightRecourse PreservedRight = "recourse"
	// RightSettlement keeps liquidation of earlier paid rights.
	RightSettlement PreservedRight = "settlement"
	// RightNewActivities unlocks new activities under an accepted
	// version. Refusals never carry it.
	RightNewActivities PreservedRight = "new-activities"
)

// RightsOf derives the rights one verdict carries: refusal limits
// new activities, never removes a paid right.
func RightsOf(decision Decision) []PreservedRight {
	rights := []PreservedRight{RightHistory, RightExport, RightRecourse, RightSettlement}
	if decision == DecisionAccepted {
		return append(rights, RightNewActivities)
	}
	return rights
}

// Consent is one express verdict of one account over one charter
// version: who decided, which published version in which language,
// what content digest was shown, which prior paid rights were on the
// table, when the verdict was recorded and the verdict itself, sealed
// by hash. The shown digest binds display to decision: a version
// swapped after display breaks the seal path before anything is
// recorded, so a forged cross-site acceptance cannot mint consent —
// the HTTP layer still binds this record with its CSRF token in T08.
type Consent struct {
	Account     string
	Version     Version
	Locale      CharterLocale
	ShownHash   string
	PriorRights []string
	DecidedAt   time.Time
	Decision    Decision
	Hash        string
}

// ConsentRequest carries one verdict attempt. Every value arrives
// from the caller: the account, the published version, the digest
// that was displayed, the prior paid rights on the table, the
// recording instant and the explicit decision.
type ConsentRequest struct {
	Account     string
	Version     string
	Locale      string
	ShownHash   string
	PriorRights []string
	DecidedAt   time.Time
	Decision    Decision
}

// parseConsentToken validates one opaque consent token: exact match,
// no control characters, bounded length.
func parseConsentToken(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidCharter
	}
	if utf8.RuneCountInString(raw) > maxConsentRunes {
		return "", ErrInvalidCharter
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", ErrInvalidCharter
		}
	}
	return raw, nil
}

// sealConsent binds account, version, locale, shown digest, prior
// rights, recording instant and decision: any reclassification
// breaks the seal first. The verdict travels as one value — seven
// loose parameters would hide the shape — and the live seal never
// enters the canonical bytes.
func sealConsent(consent Consent) string {
	canonical := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		consent.Account, consent.Version.String(), consent.Locale.String(), consent.ShownHash,
		strings.Join(consent.PriorRights, "\x00"),
		consent.DecidedAt.UTC().Format(time.RFC3339Nano), consent.Decision.String())
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// validateConsentRequest checks one verdict shape: opaque account,
// published version of an explicit locale, the digest that was
// displayed, intact prior rights and an explicit decision at a real
// instant. A version nobody published blocks before anything is
// recorded: missing versions never accept.
func validateConsentRequest(chain []Release, req ConsentRequest) (account string, release Release, prior []string, err error) {
	account, err = parseConsentToken(req.Account)
	if err != nil {
		return "", Release{}, nil, err
	}
	version, err := ParseVersion(req.Version)
	if err != nil {
		return "", Release{}, nil, err
	}
	locale, err := ParseCharterLocale(req.Locale)
	if err != nil {
		return "", Release{}, nil, err
	}
	release, err = findRelease(chain, version, locale)
	if err != nil {
		return "", Release{}, nil, err
	}
	if !isContentHash(req.ShownHash) || req.ShownHash != release.ContentHash {
		return "", Release{}, nil, ErrInvalidCharter
	}
	prior = make([]string, 0, len(req.PriorRights))
	for _, right := range req.PriorRights {
		token, err := parseConsentToken(right)
		if err != nil {
			return "", Release{}, nil, err
		}
		prior = append(prior, token)
	}
	if req.DecidedAt.IsZero() {
		return "", Release{}, nil, ErrInvalidCharter
	}
	if _, err := ParseDecision(string(req.Decision)); err != nil {
		return "", Release{}, nil, err
	}
	return account, release, prior, nil
}

// sameConsent reports whether one recorded verdict already carries
// exactly this attempt: same account, version, display, rights,
// instant and decision.
func sameConsent(recorded Consent, account string, release Release, prior []string, decided time.Time, decision Decision) bool {
	if recorded.Account != account || recorded.Version != release.Version ||
		recorded.Locale != release.Locale || recorded.ShownHash != release.ContentHash ||
		!recorded.DecidedAt.Equal(decided) || recorded.Decision != decision ||
		len(recorded.PriorRights) != len(prior) {
		return false
	}
	for i := range prior {
		if recorded.PriorRights[i] != prior[i] {
			return false
		}
	}
	return true
}

// RecordConsent records one express verdict per account and version:
// the first verdict stands, an identical replay returns it, and a
// divergent second verdict refuses with ErrConsentConflict. Change of
// mind travels via a newer published version, never by rewriting.
// Refusal preserves every prior paid right byte-identical: no paid
// right disappears, including legacy purchases.
func RecordConsent(chain []Release, ledger []Consent, req ConsentRequest) (Consent, error) {
	account, release, prior, err := validateConsentRequest(chain, req)
	if err != nil {
		return Consent{}, err
	}
	decided := req.DecidedAt.UTC()
	for _, recorded := range ledger {
		if recorded.Account != account || recorded.Version != release.Version {
			continue
		}
		if err := recorded.VerifyConsentHash(); err != nil {
			return Consent{}, err
		}
		if sameConsent(recorded, account, release, prior, decided, req.Decision) {
			return recorded, nil
		}
		return Consent{}, ErrConsentConflict
	}
	consent := Consent{
		Account: account, Version: release.Version, Locale: release.Locale,
		ShownHash: release.ContentHash, PriorRights: prior,
		DecidedAt: decided, Decision: req.Decision,
	}
	consent.Hash = sealConsent(consent)
	return consent, nil
}

// VerifyConsentHash recomputes the seal and refuses a verdict whose
// terms no longer agree: forged acceptances break here first.
func (c Consent) VerifyConsentHash() error {
	if c.Hash == "" || sealConsent(c) != c.Hash {
		return ErrInvalidCharter
	}
	return nil
}
