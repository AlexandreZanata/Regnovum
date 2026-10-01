package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func crownAnchor() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func crownCurrent(holder string, reign int) CurrentReign {
	anchor := crownAnchor()
	return CurrentReign{
		Season: "temporada-1", Holder: HolderSubject(holder), Reign: ReignVersion(reign),
		StartsAt: anchor, EndsAt: anchor.Add(7776000 * time.Second), Open: true,
	}
}

func crownSession(holder string, reign int, auth, mfa, end time.Time) SovereignSession {
	return SovereignSession{
		Season: "temporada-1", Subject: HolderSubject(holder), Reign: ReignVersion(reign),
		Competence: "cerimonial", AuthenticatedAt: auth, MFAAt: mfa, ExpiresAt: end,
	}
}

func crownClaim(act, holder string, reign int) Claim {
	return Claim{
		Act: ActID(act), Season: "temporada-1", Holder: HolderSubject(holder),
		Competence: "cerimonial", Reign: ReignVersion(reign),
	}
}

func crownCtx(now time.Time) AuthContext {
	return AuthContext{Now: now, MaxSessionAge: 30 * time.Minute}
}

func mustGrant(t *testing.T, holder string, reign int, now time.Time) Grant {
	t.Helper()
	auth := now.Add(-5 * time.Minute)
	grant, err := AuthorizeAct(
		crownClaim("ato-1", holder, reign),
		crownSession(holder, reign, auth, auth.Add(time.Minute), auth.Add(20*time.Minute)),
		nil, crownCurrent(holder, reign), crownCtx(now),
	)
	if err != nil {
		t.Fatalf("AuthorizeAct: %v", err)
	}
	return grant
}

func TestAuthorityShape(t *testing.T) {
	for _, raw := range []string{"", "  ", "com\ntrole"} {
		if _, err := ParseHolderSubject(raw); err == nil {
			t.Fatalf("holder %q passed", raw)
		}
	}
	if _, err := ParseCompetence(strings.Repeat("a", maxCrownRunes+1)); err == nil {
		t.Fatal("overlong competence passed")
	}
	if _, err := ParseReignVersion(0); err == nil {
		t.Fatal("zero reign passed")
	}
	if _, err := ParseReignVersion(-2); err == nil {
		t.Fatal("negative reign passed")
	}
	claim := crownClaim("ato-1", "rainha-1", 1)
	claim.Holder = ""
	if _, err := AuthorizeAct(claim, SovereignSession{}, nil, crownCurrent("rainha-1", 1), crownCtx(crownAnchor().Add(time.Hour))); err == nil {
		t.Fatal("partial credential passed: holderless claims decide nothing")
	}
}

func TestAuthorizeActGrantsCurrentHolder(t *testing.T) {
	now := crownAnchor().Add(time.Hour)
	grant := mustGrant(t, "rainha-1", 1, now)
	if grant.Holder != "rainha-1" || grant.Reign != 1 || grant.Season != "temporada-1" {
		t.Fatalf("grant = %+v, want the invested office", grant)
	}
	if !grant.ExpiresAt.After(now) {
		t.Fatalf("grant expires %v at %v: grants outlive the call, never the session", grant.ExpiresAt, now)
	}
	if err := RevalidateGrant(grant, crownCurrent("rainha-1", 1), now); err != nil {
		t.Fatalf("RevalidateGrant: %v", err)
	}
}

func TestAuthorizeActRefusesPartialCredential(t *testing.T) {
	now := crownAnchor().Add(time.Hour)
	current := crownCurrent("rainha-1", 1)
	auth := now.Add(-5 * time.Minute)
	full := crownSession("rainha-1", 1, auth, auth.Add(time.Minute), auth.Add(20*time.Minute))
	claim := crownClaim("ato-1", "rainha-1", 1)
	if _, err := AuthorizeAct(claim, SovereignSession{}, nil, current, crownCtx(now)); err == nil {
		t.Fatal("missing session passed")
	}
	withoutMFA := full
	withoutMFA.MFAAt = time.Time{}
	if _, err := AuthorizeAct(claim, withoutMFA, nil, current, crownCtx(now)); err == nil {
		t.Fatal("session without step-up passed: MFA is mandatory")
	}
	foreign := full
	foreign.Subject = "rainha-1"
	foreign.Competence = "tesouro"
	if _, err := AuthorizeAct(claim, foreign, nil, current, crownCtx(now)); err == nil {
		t.Fatal("competence-mismatched session passed")
	}
}

func TestAuthorizeActRefusesClosedSeason(t *testing.T) {
	anchor := crownAnchor()
	current := crownCurrent("rainha-1", 1)
	at := func(offset time.Duration) (Grant, error) {
		now := anchor.Add(offset)
		auth := now.Add(-5 * time.Minute)
		return AuthorizeAct(
			crownClaim("ato-1", "rainha-1", 1),
			crownSession("rainha-1", 1, auth, auth.Add(time.Minute), auth.Add(20*time.Minute)),
			nil, current, crownCtx(now),
		)
	}
	if _, err := at(-time.Nanosecond); !errors.Is(err, ErrSeasonClosed) {
		t.Fatalf("before start = %v, want ErrSeasonClosed", err)
	}
	if _, err := at(0); err != nil {
		t.Fatalf("at start: %v", err)
	}
	if _, err := at(7776000*time.Second - time.Nanosecond); err != nil {
		t.Fatalf("tick before end: %v", err)
	}
	if _, err := at(7776000 * time.Second); !errors.Is(err, ErrSeasonClosed) {
		t.Fatalf("exact end = %v, want ErrSeasonClosed: the end belongs out", err)
	}
	suspended := current
	suspended.Open = false
	now := anchor.Add(time.Hour)
	auth := now.Add(-5 * time.Minute)
	if _, err := AuthorizeAct(crownClaim("ato-1", "rainha-1", 1),
		crownSession("rainha-1", 1, auth, auth.Add(time.Minute), auth.Add(20*time.Minute)),
		nil, suspended, crownCtx(now)); !errors.Is(err, ErrSeasonClosed) {
		t.Fatalf("suspended = %v, want ErrSeasonClosed", err)
	}
	other := crownClaim("ato-1", "rainha-1", 1)
	other.Season = "temporada-2"
	if _, err := AuthorizeAct(other,
		crownSession("rainha-1", 1, auth, auth.Add(time.Minute), auth.Add(20*time.Minute)),
		nil, current, crownCtx(now)); !errors.Is(err, ErrSeasonMismatch) {
		t.Fatalf("other book = %v, want ErrSeasonMismatch", err)
	}
}

func TestAuthorizeActRefusesStaleReignAndRevokesHolder(t *testing.T) {
	anchor := crownAnchor()
	now := anchor.Add(time.Hour)
	auth := now.Add(-5 * time.Minute)
	session := func(holder string, reign int) SovereignSession {
		return crownSession(holder, reign, auth, auth.Add(time.Minute), auth.Add(20*time.Minute))
	}
	old := crownCurrent("rainha-1", 1)
	next := crownCurrent("rainha-2", 2)
	if _, err := AuthorizeAct(crownClaim("ato-1", "rainha-1", 2), session("rainha-1", 2), nil, next, crownCtx(now)); !errors.Is(err, ErrNotHolder) {
		t.Fatalf("superseded holder = %v, want ErrNotHolder: investiture revokes", err)
	}
	if _, err := AuthorizeAct(crownClaim("ato-1", "rainha-1", 1), session("rainha-1", 1), nil, next, crownCtx(now)); !errors.Is(err, ErrStaleReign) {
		t.Fatalf("old reign = %v, want ErrStaleReign", err)
	}
	_ = old
	if _, err := AuthorizeAct(crownClaim("ato-1", "rainha-2", 3), session("rainha-2", 3), nil, next, crownCtx(now)); !errors.Is(err, ErrFutureReign) {
		t.Fatalf("future reign = %v, want ErrFutureReign", err)
	}
	grant, err := AuthorizeAct(crownClaim("ato-2", "rainha-2", 2), session("rainha-2", 2), nil, next, crownCtx(now))
	if err != nil {
		t.Fatalf("new holder: %v", err)
	}
	if err := RevalidateGrant(grant, next, now); err != nil {
		t.Fatalf("new grant: %v", err)
	}
}

func TestAuthorizeActDelegatesWithinWindow(t *testing.T) {
	anchor := crownAnchor()
	now := anchor.Add(time.Hour)
	current := crownCurrent("rainha-1", 1)
	auth := now.Add(-5 * time.Minute)
	delegateSession := crownSession("operadora-1", 1, auth, auth.Add(time.Minute), auth.Add(20*time.Minute))
	chain := &Delegation{
		Delegator: "rainha-1", Delegate: "operadora-1", Season: "temporada-1",
		Competence: "cerimonial", Reign: 1, ExpiresAt: now.Add(time.Hour),
	}
	if _, err := AuthorizeAct(crownClaim("ato-1", "operadora-1", 1), delegateSession, chain, current, crownCtx(now)); err != nil {
		t.Fatalf("live delegation: %v", err)
	}
	spent := *chain
	spent.ExpiresAt = now.Add(-time.Nanosecond)
	if _, err := AuthorizeAct(crownClaim("ato-1", "operadora-1", 1), delegateSession, &spent, current, crownCtx(now)); !errors.Is(err, ErrDelegationExpired) {
		t.Fatalf("spent delegation = %v, want ErrDelegationExpired", err)
	}
	widened := *chain
	widened.Competence = "tesouro"
	if _, err := AuthorizeAct(crownClaim("ato-1", "operadora-1", 1), delegateSession, &widened, current, crownCtx(now)); !errors.Is(err, ErrInvalidDelegation) {
		t.Fatalf("widened delegation = %v, want ErrInvalidDelegation", err)
	}
	stranger := *chain
	stranger.Delegator = "rainha-9"
	if _, err := AuthorizeAct(crownClaim("ato-1", "operadora-1", 1), delegateSession, &stranger, current, crownCtx(now)); !errors.Is(err, ErrInvalidDelegation) {
		t.Fatalf("stranger chain = %v, want ErrInvalidDelegation", err)
	}
	if _, err := AuthorizeAct(crownClaim("ato-1", "rainha-1", 1), delegateSession, chain, current, crownCtx(now)); !errors.Is(err, ErrInvalidDelegation) {
		t.Fatalf("self theater = %v, want ErrInvalidDelegation", err)
	}
}

func TestAuthorizeActRefusesOperatorKing(t *testing.T) {
	now := crownAnchor().Add(time.Hour)
	auth := now.Add(-5 * time.Minute)
	claim := crownClaim("ato-1", "admin-1", 1)
	claim.Operator = true
	current := CurrentReign{
		Season: "temporada-1", Holder: "admin-1", Reign: 1,
		StartsAt: crownAnchor(), EndsAt: crownAnchor().Add(7776000 * time.Second), Open: true,
	}
	if _, err := AuthorizeAct(claim,
		crownSession("admin-1", 1, auth, auth.Add(time.Minute), auth.Add(20*time.Minute)),
		nil, current, crownCtx(now)); !errors.Is(err, ErrOperatorNotSovereign) {
		t.Fatalf("operator king = %v, want ErrOperatorNotSovereign", err)
	}
}

func TestGrantReplayRefuses(t *testing.T) {
	now := crownAnchor().Add(time.Hour)
	current := crownCurrent("rainha-1", 1)
	grant := mustGrant(t, "rainha-1", 1, now)
	past := grant.ExpiresAt.Add(time.Nanosecond)
	if err := RevalidateGrant(grant, current, past); !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("spent grant = %v, want ErrGrantExpired: replay authorizes nothing new", err)
	}
	moved := crownCurrent("rainha-2", 2)
	if err := RevalidateGrant(grant, moved, now); !errors.Is(err, ErrStaleReign) {
		t.Fatalf("moved reign = %v, want ErrStaleReign", err)
	}
	fresh, err := AuthorizeAct(crownClaim("ato-1", "rainha-1", 1),
		crownSession("rainha-1", 1, now.Add(-time.Minute), now, now.Add(20*time.Minute)),
		nil, current, crownCtx(now.Add(time.Minute)))
	if err != nil {
		t.Fatalf("live re-authorize: %v", err)
	}
	if fresh.Act != grant.Act || fresh.ExpiresAt.Before(now) {
		t.Fatalf("re-authorized = %+v, want the same act bound to a live session", fresh)
	}
}

func TestSessionStaysShortAndOrdered(t *testing.T) {
	now := crownAnchor().Add(time.Hour)
	current := crownCurrent("rainha-1", 1)
	claim := crownClaim("ato-1", "rainha-1", 1)
	auth := now.Add(-5 * time.Minute)
	overlong := crownSession("rainha-1", 1, auth, auth.Add(time.Minute), auth.Add(time.Hour))
	if _, err := AuthorizeAct(claim, overlong, nil, current, crownCtx(now)); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("overlong session = %v, want ErrInvalidSession: sessions stay short", err)
	}
	future := crownSession("rainha-1", 1, now.Add(time.Hour), now.Add(time.Hour+time.Minute), now.Add(2*time.Hour))
	if _, err := AuthorizeAct(claim, future, nil, current, crownCtx(now)); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("future session = %v, want ErrInvalidSession", err)
	}
	backward := crownSession("rainha-1", 1, auth, auth.Add(-time.Minute), auth.Add(20*time.Minute))
	if _, err := AuthorizeAct(claim, backward, nil, current, crownCtx(now)); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("unordered step-up = %v, want ErrInvalidSession", err)
	}
	uncapped := AuthContext{Now: now}
	if err := uncapped.valid(); err == nil {
		t.Fatal("uncapped context passed: the session cap arrives per call")
	}
}
