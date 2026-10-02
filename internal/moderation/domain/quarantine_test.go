package domain_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

func quarantineAnchor() time.Time {
	return time.Date(2026, time.February, 6, 10, 0, 0, 0, time.UTC)
}

func concealRequest(id, owner, concealer, urgency string) domain.ConcealRequest {
	return domain.ConcealRequest{
		ID: "medida-" + id, CaseRef: "caso-1", PieceID: "peca-1",
		Owner: domain.AccountID(owner), Accused: "ana",
		Concealer: concealer, Urgency: urgency, At: quarantineAnchor(),
	}
}

func suspendRequest(id, account, imposer string) domain.SuspendRequest {
	return domain.SuspendRequest{
		ID: "medida-" + id, CaseRef: "caso-1",
		Account: domain.AccountID(account), Accused: "ana",
		Imposer: imposer, Motive: "risco concreto de fuga da prova",
		At: quarantineAnchor(),
	}
}

func quarantineRequest(id, account, sentence string, sentencedAt, endsAt time.Time) domain.QuarantineRequest {
	return domain.QuarantineRequest{
		ID: "medida-" + id, CaseRef: "caso-1",
		Account: domain.AccountID(account), Accused: "ana",
		SentenceRef: sentence, SentencedAt: sentencedAt,
		Imposer: "arbitro-1", Motive: "cumprimento da pena após condenação",
		At: quarantineAnchor(), EndsAt: endsAt, MaxQuarantine: 168 * time.Hour,
	}
}

func TestEmergencyConcealmentAndSuspensionWithReview(t *testing.T) {
	t.Parallel()

	if !domain.MeasureConcealment.IsEmergency() || !domain.MeasurePreventiveSuspension.IsEmergency() {
		t.Fatal("concealment and preventive suspension must read as emergency")
	}
	if domain.MeasureQuarantine.IsEmergency() {
		t.Fatal("post-conviction quarantine must not read as emergency")
	}

	hidden, err := domain.ConcealPiece(concealRequest("1", "ana", domain.OfficeJusticeiro, "vazamento em curso da peça selada"))
	if err != nil {
		t.Fatalf("ConcealPiece: %v", err)
	}
	if hidden.IsFinal() {
		t.Fatal("concealment must never read as final guilt")
	}

	suspended, err := domain.SuspendPreventively(suspendRequest("2", "ana", "justiceiro-1"))
	if err != nil {
		t.Fatalf("SuspendPreventively: %v", err)
	}
	if suspended.IsFinal() {
		t.Fatal("preventive suspension must never read as final guilt")
	}
	if !suspended.ReviewDeadline.Equal(quarantineAnchor().Add(domain.EmergencyReviewWindow)) {
		t.Fatalf("deadline = %v, want the 24h review", suspended.ReviewDeadline)
	}

	reviewed, err := domain.ReviewSuspension(suspended, "auditor-1", false, quarantineAnchor().Add(23*time.Hour))
	if err != nil {
		t.Fatalf("ReviewSuspension: %v", err)
	}
	if !reviewed.Reviewed || reviewed.Reverted {
		t.Fatalf("review = %+v, want a confirmed suspension", reviewed)
	}
	if !domain.SuspensionActive(reviewed, quarantineAnchor().Add(23*time.Hour+30*time.Minute)) {
		t.Fatal("a confirmed suspension must stand inside its window")
	}

	held, err := domain.QuarantineAfterConviction(quarantineRequest("3", "ana", "sentenca-1", quarantineAnchor().Add(-time.Hour), quarantineAnchor().Add(72*time.Hour)))
	if err != nil {
		t.Fatalf("QuarantineAfterConviction: %v", err)
	}
	if !held.IsFinal() {
		t.Fatal("post-conviction quarantine must follow final guilt")
	}
}

func TestPreventiveReviewTimeoutAndExactTick(t *testing.T) {
	t.Parallel()

	suspended, err := domain.SuspendPreventively(suspendRequest("1", "ana", "justiceiro-1"))
	if err != nil {
		t.Fatalf("SuspendPreventively: %v", err)
	}

	if _, err := domain.ReviewSuspension(suspended, "auditor-1", false, suspended.ReviewDeadline); !errors.Is(err, domain.ErrReviewTimeout) {
		t.Fatalf("review at the exact tick = %v, want ErrReviewTimeout", err)
	}
	if _, err := domain.ReviewSuspension(suspended, "auditor-1", false, suspended.ReviewDeadline.Add(time.Hour)); !errors.Is(err, domain.ErrReviewTimeout) {
		t.Fatalf("late review = %v, want ErrReviewTimeout", err)
	}
	if domain.SuspensionActive(suspended, suspended.ReviewDeadline) {
		t.Fatal("the measure must lapse at the exact tick")
	}
	if !domain.SuspensionActive(suspended, suspended.ReviewDeadline.Add(-time.Nanosecond)) {
		t.Fatal("one nanosecond before the deadline the measure must still stand")
	}
	if domain.SuspensionActive(suspended, suspended.ReviewDeadline.Add(time.Hour)) {
		t.Fatal("an unreviewed measure must not survive its deadline")
	}
}

func TestThirdPartyContentIsUntouchable(t *testing.T) {
	t.Parallel()

	if _, err := domain.ConcealPiece(concealRequest("1", "terceiro-1", domain.OfficeJusticeiro, "urgência concreta")); !errors.Is(err, domain.ErrThirdPartyTarget) {
		t.Fatalf("third-party piece = %v, want ErrThirdPartyTarget", err)
	}
	if _, err := domain.SuspendPreventively(suspendRequest("2", "terceiro-1", "justiceiro-1")); !errors.Is(err, domain.ErrThirdPartyTarget) {
		t.Fatalf("third-party account = %v, want ErrThirdPartyTarget", err)
	}
	if _, err := domain.QuarantineAfterConviction(quarantineRequest("3", "terceiro-1", "sentenca-1", quarantineAnchor().Add(-time.Hour), quarantineAnchor().Add(time.Hour))); !errors.Is(err, domain.ErrThirdPartyTarget) {
		t.Fatalf("third-party quarantine = %v, want ErrThirdPartyTarget", err)
	}
	if _, err := domain.ConcealPiece(concealRequest("4", "ana", "inquisidor-1", "urgência concreta")); !errors.Is(err, domain.ErrRoleNotAuthorized) {
		t.Fatalf("non-justiceiro concealer = %v, want ErrRoleNotAuthorized", err)
	}
	if _, err := domain.ConcealPiece(concealRequest("5", "ana", domain.OfficeJusticeiro, "  ")); !errors.Is(err, domain.ErrInvalidJustification) {
		t.Fatalf("vague urgency = %v, want ErrInvalidJustification", err)
	}
}

func TestReversalAndAppealRestoreWithoutGuilt(t *testing.T) {
	t.Parallel()

	hidden, err := domain.ConcealPiece(concealRequest("1", "ana", domain.OfficeJusticeiro, "urgência concreta"))
	if err != nil {
		t.Fatalf("ConcealPiece: %v", err)
	}
	if _, err := domain.ReverseConcealment(hidden, domain.OfficeJusticeiro); !errors.Is(err, domain.ErrConflictOfInterest) {
		t.Fatalf("self reversal = %v, want ErrConflictOfInterest", err)
	}
	restored, err := domain.ReverseConcealment(hidden, "auditor-1")
	if err != nil {
		t.Fatalf("ReverseConcealment: %v", err)
	}
	if !restored.Reverted || restored.IsFinal() {
		t.Fatalf("restored = %+v, want a reverted piece that never read as guilt", restored)
	}

	suspended, err := domain.SuspendPreventively(suspendRequest("2", "ana", "justiceiro-1"))
	if err != nil {
		t.Fatalf("SuspendPreventively: %v", err)
	}
	if _, _, err := domain.AppealSuspension(suspended, "ana", "justiceiro-1", domain.OutcomeReversed, quarantineAnchor().Add(time.Hour)); !errors.Is(err, domain.ErrConflictOfInterest) {
		t.Fatalf("imposer reviewing = %v, want ErrConflictOfInterest", err)
	}
	appeal, lifted, err := domain.AppealSuspension(suspended, "ana", "auditor-1", domain.OutcomeReversed, quarantineAnchor().Add(time.Hour))
	if err != nil {
		t.Fatalf("AppealSuspension: %v", err)
	}
	if appeal.Outcome != domain.OutcomeReversed || !lifted.Reverted {
		t.Fatalf("appeal = %+v lifted = %+v, want the reversal recorded", appeal, lifted)
	}
	if domain.SuspensionActive(lifted, quarantineAnchor().Add(2*time.Hour)) {
		t.Fatal("a reversed suspension must not stand")
	}
	if _, _, err := domain.AppealConcealment(hidden, "terceiro-1", "auditor-1", domain.OutcomeReversed, quarantineAnchor().Add(time.Hour)); !errors.Is(err, domain.ErrThirdPartyTarget) {
		t.Fatalf("stranger appeal = %v, want ErrThirdPartyTarget", err)
	}
}

func TestPreventiveMeasureCarriesNoFinalGuilt(t *testing.T) {
	t.Parallel()

	for _, value := range []any{domain.Concealment{}, domain.PreventiveSuspension{}} {
		fields := []string{}
		for i := 0; i < reflect.TypeOf(value).NumField(); i++ {
			fields = append(fields, reflect.TypeOf(value).Field(i).Name)
		}
		for _, banned := range []string{"Guilt", "Sanction", "Final", "Verdict", "Conviction"} {
			for _, field := range fields {
				if field == banned {
					t.Fatalf("%T carries %s: a preventive measure never stores final guilt", value, field)
				}
			}
		}
	}
	if _, err := domain.QuarantineAfterConviction(quarantineRequest("1", "ana", "", quarantineAnchor().Add(-time.Hour), quarantineAnchor().Add(time.Hour))); !errors.Is(err, domain.ErrMissingConviction) {
		t.Fatalf("quarantine without sentence = %v, want ErrMissingConviction", err)
	}
	early := quarantineRequest("2", "ana", "sentenca-1", quarantineAnchor().Add(time.Hour), quarantineAnchor().Add(2*time.Hour))
	if _, err := domain.QuarantineAfterConviction(early); !errors.Is(err, domain.ErrMissingConviction) {
		t.Fatalf("quarantine before sentence = %v, want ErrMissingConviction", err)
	}
	pastCap := quarantineRequest("3", "ana", "sentenca-1", quarantineAnchor().Add(-time.Hour), quarantineAnchor().Add(169*time.Hour))
	if _, err := domain.QuarantineAfterConviction(pastCap); !errors.Is(err, domain.ErrExcessiveQuarantine) {
		t.Fatalf("quarantine past the cap = %v, want ErrExcessiveQuarantine", err)
	}
}
