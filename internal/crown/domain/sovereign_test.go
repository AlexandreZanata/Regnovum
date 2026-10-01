package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func sovAnchor() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func sovCurrent() CurrentReign {
	anchor := sovAnchor()
	return CurrentReign{
		Season: "temporada-1", Holder: "rainha-1", Reign: 2,
		StartsAt: anchor, EndsAt: anchor.Add(7776000 * time.Second), Open: true,
	}
}

func sovAct() RoyalAct {
	anchor := sovAnchor()
	return RoyalAct{
		ID: "decreto-90", Author: "rainha-1", Season: "temporada-1",
		Reign: 2, Competence: "patrimonial", Kind: ActEconomic,
		Reason: "reparacao devida com origem identificada",
		Target: "conta/ana", Effect: "transferir 1000 de tesouro-livre sem mint",
		DecreedAt: anchor, Effective: anchor.Add(time.Hour),
		EndsAt:  anchor.Add(30 * 24 * time.Hour),
		Charter: "v3", Origin: "tesouro-livre", Amount: 1000,
	}
}

func sovClaim(act RoyalAct) Claim {
	return Claim{
		Act: act.ID, Season: act.Season, Holder: act.Author,
		Competence: act.Competence, Reign: act.Reign,
	}
}

func sovSession(act RoyalAct, now time.Time) SovereignSession {
	auth := now.Add(-5 * time.Minute)
	return SovereignSession{
		Season: act.Season, Subject: act.Author, Reign: act.Reign,
		Competence:      act.Competence,
		AuthenticatedAt: auth, MFAAt: auth.Add(time.Minute), ExpiresAt: auth.Add(20 * time.Minute),
	}
}

func sovCtx(now time.Time) AuthContext {
	return AuthContext{Now: now, MaxSessionAge: 30 * time.Minute}
}

func sovApprovals(t *testing.T, act RoyalAct, now time.Time) (Approval, Approval) {
	t.Helper()
	digest, err := ActDigest(act)
	if err != nil {
		t.Fatalf("ActDigest: %v", err)
	}
	checked := now.Add(-10 * time.Minute)
	first := Approval{
		ID: "conf-1", Act: act.ID, Checker: "auditor-1",
		Season: act.Season, Reign: act.Reign, Digest: digest,
		CheckedAt: checked, ExpiresAt: now.Add(time.Hour),
	}
	second := Approval{
		ID: "conf-2", Act: act.ID, Checker: "auditor-2",
		Season: act.Season, Reign: act.Reign, Digest: digest,
		CheckedAt: checked, ExpiresAt: now.Add(time.Hour),
	}
	return first, second
}

func sovClearance(t *testing.T, act RoyalAct, now time.Time) Clearance {
	t.Helper()
	first, second := sovApprovals(t, act, now)
	clearance, err := RequireIndependentCheck(act, first, second, sovCurrent(), now)
	if err != nil {
		t.Fatalf("RequireIndependentCheck: %v", err)
	}
	return clearance
}

func sovCustody() CustodyView {
	return CustodyView{Origin: "tesouro-livre", Beneficiary: "ana", Available: 5000}
}

func sovPanel() Panel {
	return Panel{
		Proceeding: "processo-90", Season: "temporada-1", Reign: 2,
		Inquisitor: "inquisidor-1", Accuser: "acusador-1", Accused: "ana",
		Judge: "juiz-1", Defender: "defensor-1",
		Witnesses: []HolderSubject{"testemunha-1"},
		Executor:  "carrasco-1", Auditor: "auditor-1",
		Interested: []HolderSubject{"lesada-1"},
	}
}

func TestSovereignAdversarialAuthorizesConservedPath(t *testing.T) {
	act := sovAct()
	now := sovAnchor().Add(2 * time.Hour)
	current := sovCurrent()
	if _, err := AuthorizeAct(sovClaim(act), sovSession(act, now), nil, current, sovCtx(now)); err != nil {
		t.Fatalf("AuthorizeAct: %v", err)
	}
	clearance := sovClearance(t, act, now)
	order, err := PlanExecution(act, clearance, current, sovCustody(), now)
	if err != nil {
		t.Fatalf("PlanExecution: %v", err)
	}
	receipt, err := SealReceipt(order)
	if err != nil {
		t.Fatalf("SealReceipt: %v", err)
	}
	if receipt.Act != act.ID || receipt.Season != act.Season || receipt.Reign != act.Reign || receipt.Digest != order.Digest {
		t.Fatalf("receipt = %+v, want the same act/book/reign/digest", receipt)
	}
	var book RoyalBook
	record, err := Summarize(act, "")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if err := book.Append(record); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := AuditCoverage([]ActID{act.ID}, book); err != nil {
		t.Fatalf("AuditCoverage: %v", err)
	}
	if _, err := SeatPanel(sovPanel(), current, now); err != nil {
		t.Fatalf("SeatPanel: %v", err)
	}
}

func TestSovereignAdversarialRefusesActorAndSessionCells(t *testing.T) {
	act := sovAct()
	now := sovAnchor().Add(2 * time.Hour)
	cases := map[string]func() error{
		"operator alias never sovereign": func() error {
			claim := sovClaim(act)
			claim.Operator = true
			_, err := AuthorizeAct(claim, sovSession(act, now), nil, sovCurrent(), sovCtx(now))
			return err
		},
		"stranger decides nothing": func() error {
			claim := sovClaim(act)
			claim.Holder = "intruso-1"
			_, err := AuthorizeAct(claim, sovSession(act, now), nil, sovCurrent(), sovCtx(now))
			return err
		},
		"other season decides nothing": func() error {
			claim := sovClaim(act)
			claim.Season = "temporada-2"
			_, err := AuthorizeAct(claim, sovSession(act, now), nil, sovCurrent(), sovCtx(now))
			return err
		},
		"closed season decides nothing": func() error {
			current := sovCurrent()
			current.Open = false
			_, err := AuthorizeAct(sovClaim(act), sovSession(act, now), nil, current, sovCtx(now))
			return err
		},
		"missing MFA decides nothing": func() error {
			session := sovSession(act, now)
			session.MFAAt = time.Time{}
			_, err := AuthorizeAct(sovClaim(act), session, nil, sovCurrent(), sovCtx(now))
			return err
		},
		"oversized session decides nothing": func() error {
			session := sovSession(act, now)
			auth := now.Add(-5 * time.Minute)
			session.ExpiresAt = auth.Add(time.Hour)
			_, err := AuthorizeAct(sovClaim(act), session, nil, sovCurrent(), sovCtx(now))
			return err
		},
	}
	wants := map[string]error{
		"operator alias never sovereign":    ErrOperatorNotSovereign,
		"stranger decides nothing":          ErrNotHolder,
		"other season decides nothing":      ErrSeasonMismatch,
		"closed season decides nothing":     ErrSeasonClosed,
		"missing MFA decides nothing":       ErrInvalidAuthority,
		"oversized session decides nothing": ErrInvalidSession,
	}
	for name, run := range cases {
		if err := run(); !errors.Is(err, wants[name]) {
			t.Fatalf("%s = %v, want %v", name, err, wants[name])
		}
	}
}

func TestSovereignAdversarialRefusesCheckAndRecordCells(t *testing.T) {
	act := sovAct()
	now := sovAnchor().Add(2 * time.Hour)
	first, second := sovApprovals(t, act, now)
	if _, err := RequireIndependentCheck(act, first, first, sovCurrent(), now); !errors.Is(err, ErrDuplicateApproval) {
		t.Fatalf("replayed approval = %v, want ErrDuplicateApproval", err)
	}
	self := first
	self.Checker = act.Author
	if _, err := RequireIndependentCheck(act, self, second, sovCurrent(), now); !errors.Is(err, ErrSelfApproval) {
		t.Fatalf("self approval = %v, want ErrSelfApproval", err)
	}
	tampered := second
	tampered.Digest = first.Digest[:63] + "0"
	if tampered.Digest == first.Digest {
		tampered.Digest = first.Digest[:63] + "1"
	}
	if _, err := RequireIndependentCheck(act, first, tampered, sovCurrent(), now); !errors.Is(err, ErrTamperedAct) {
		t.Fatalf("tampered payload = %v, want ErrTamperedAct", err)
	}
	stale := sovCurrent()
	stale.Holder = "nova-1"
	stale.Reign = 3
	if _, err := RequireIndependentCheck(act, first, second, stale, now); !errors.Is(err, ErrStaleReign) {
		t.Fatalf("ex-king clearance = %v, want ErrStaleReign", err)
	}
	after := now.Add(2 * time.Hour)
	if _, err := RequireIndependentCheck(act, first, second, sovCurrent(), after); !errors.Is(err, ErrCheckExpired) {
		t.Fatalf("spent check = %v, want ErrCheckExpired", err)
	}
	urgent := first
	urgent.Emergency = true
	if _, err := RequireIndependentCheck(act, urgent, second, sovCurrent(), now); !errors.Is(err, ErrEmergencyWithoutReview) {
		t.Fatalf("urgency without review = %v, want ErrEmergencyWithoutReview", err)
	}
	var book RoyalBook
	if err := AuditCoverage([]ActID{act.ID}, book); !errors.Is(err, ErrUnrecordedAct) {
		t.Fatalf("hidden act = %v, want ErrUnrecordedAct", err)
	}
}

func TestSovereignAdversarialRefusesCustodyCells(t *testing.T) {
	act := sovAct()
	now := sovAnchor().Add(2 * time.Hour)
	clearance := sovClearance(t, act, now)
	cases := map[string]CustodyView{
		"forbidden origin funds nothing":   {Origin: "escrow", Beneficiary: "ana", Available: 5000},
		"self grant moves nothing":         {Origin: "tesouro-livre", Beneficiary: act.Author, Available: 5000},
		"short treasury moves nothing":     {Origin: "tesouro-livre", Beneficiary: "ana", Available: 999},
		"frozen book moves nothing":        {Origin: "tesouro-livre", Beneficiary: "ana", Available: 5000, Frozen: true},
		"conflicted benefit moves nothing": {Origin: "tesouro-livre", Beneficiary: "ana", Available: 5000, ConflictPending: true},
	}
	wants := map[string]error{
		"forbidden origin funds nothing":   ErrForbiddenOrigin,
		"self grant moves nothing":         ErrSelfGrant,
		"short treasury moves nothing":     ErrInsufficientTreasury,
		"frozen book moves nothing":        ErrExecutionFrozen,
		"conflicted benefit moves nothing": ErrConflictedBenefit,
	}
	for name, custody := range cases {
		if _, err := PlanExecution(act, clearance, sovCurrent(), custody, now); !errors.Is(err, wants[name]) {
			t.Fatalf("%s = %v, want %v", name, err, wants[name])
		}
	}
	plain := act
	plain.Kind = ActNormative
	plain.Origin = ""
	plain.Amount = 0
	if _, err := PlanExecution(plain, clearance, sovCurrent(), sovCustody(), now); !errors.Is(err, ErrNonMonetaryAct) {
		t.Fatalf("non-monetary execution = %v, want ErrNonMonetaryAct", err)
	}
}

func TestSovereignAdversarialRefusesConflictCells(t *testing.T) {
	now := sovAnchor().Add(2 * time.Hour)
	current := sovCurrent()
	accuser := sovPanel()
	accuser.Judge = accuser.Accuser
	if _, err := SeatPanel(accuser, current, now); !errors.Is(err, ErrConflictedOffice) {
		t.Fatalf("accuser judge = %v, want ErrConflictedOffice", accuser)
	}
	executor := sovPanel()
	executor.Executor = "lesada-1"
	if _, err := SeatPanel(executor, current, now); !errors.Is(err, ErrConflictedOffice) {
		t.Fatalf("interested executor = %v, want ErrConflictedOffice", executor)
	}
	king := sovPanel()
	king.Accuser = current.Holder
	king.Judge = current.Holder
	if _, err := SeatPanel(king, current, now); !errors.Is(err, ErrConflictedOffice) {
		t.Fatalf("king judging rival = %v, want ErrConflictedOffice", king)
	}
	outcome := Honorarium{
		ID: "honorario-90", Proceeding: "processo-90", Recipient: "juiz-1",
		Office: OfficeJustice, Season: "temporada-1", Reign: 2,
		Service: "presidir audiencia", Amount: 500, ForConviction: true,
	}
	if _, err := AuthorizeHonorarium(outcome); !errors.Is(err, ErrOutcomePay) {
		t.Fatalf("conviction pay = %v, want ErrOutcomePay", outcome)
	}
	book := appealBook(t)
	filing, err := FilePetition(book, "peticao-90", "decreto-40", "ana", "valor devido a menor", appealAnchor().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("FilePetition: %v", err)
	}
	review, err := ReviewPetition(book, filing, ReviewInput{ID: "revisao-90", Auditor: "auditor-1", Verdict: VerdictRepair, Harm: true, Responsible: "rainha-1", Harmed: "ana", At: appealAnchor().Add(3 * time.Hour)})
	if err != nil {
		t.Fatalf("ReviewPetition: %v", err)
	}
	treasury := appealRestitution()
	treasury.Act = "decreto-40"
	treasury.Corrective = "decreto-41"
	treasury.Origin = "tesouro-livre"
	if _, err := OrderRepair(appealCorrective(), review, &treasury); !errors.Is(err, ErrTreasuryFundedRestitution) {
		t.Fatalf("treasury restitution = %v, want ErrTreasuryFundedRestitution", err)
	}
}

func TestSovereignAdversarialPublicLogCarriesNoPII(t *testing.T) {
	act := sovAct()
	record, err := Summarize(act, "")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	canonical := record.Canonical()
	for _, secret := range []string{act.Reason, act.Target, act.Effect, string(sovCustody().Beneficiary), act.Origin, "1000"} {
		if secret != "" && strings.Contains(canonical, secret) {
			t.Fatalf("public log exposes %q: victims and payload stay in the protected annex", secret)
		}
	}
	if !isDigest(record.Digest) {
		t.Fatalf("digest = %q, want 64 lowercase hex without proof", record.Digest)
	}
	ordinary, err := DefineAppointment(Appointment{
		ID: "nomeacao-90", Office: OfficeJustice, Holder: "juiz-1",
		Season: "temporada-1", Reign: 2, Competence: "julgar",
		Appointer: "rainha-1", StartsAt: sovAnchor(), EndsAt: sovAnchor().Add(24 * time.Hour),
	}, sovCurrent())
	if err != nil {
		t.Fatalf("DefineAppointment: %v", err)
	}
	now := sovAnchor().Add(2 * time.Hour)
	claim := sovClaim(act)
	claim.Holder = ordinary.Holder
	if _, err := AuthorizeAct(claim, sovSession(act, now), nil, sovCurrent(), sovCtx(now)); !errors.Is(err, ErrNotHolder) {
		t.Fatalf("ordinary office = %v, want ErrNotHolder: offices never carry sovereignty", err)
	}
}
