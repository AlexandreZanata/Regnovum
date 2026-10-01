package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

type fakeReigns struct {
	current domain.CurrentReign
	err     error
	calls   int
}

func (f *fakeReigns) Current(_ context.Context, season domain.SeasonID) (domain.CurrentReign, error) {
	f.calls++
	if f.err != nil {
		return domain.CurrentReign{}, f.err
	}
	if season != f.current.Season {
		return domain.CurrentReign{}, domain.ErrSeasonMismatch
	}
	return f.current, nil
}

func reignAnchor() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func reignSnapshot() domain.CurrentReign {
	anchor := reignAnchor()
	return domain.CurrentReign{
		Season: "temporada-1", Holder: "rainha-1", Reign: 1,
		StartsAt: anchor, EndsAt: anchor.Add(7776000 * time.Second), Open: true,
	}
}

func reignCommand(holder string, reign int, now time.Time) AuthorizeCommand {
	auth := now.Add(-5 * time.Minute)
	return AuthorizeCommand{
		Act: "ato-1", Season: "temporada-1", Holder: holder,
		Competence: "cerimonial", Reign: reign,
		Session: domain.SovereignSession{
			Season: "temporada-1", Subject: domain.HolderSubject(holder), Reign: domain.ReignVersion(reign),
			Competence: "cerimonial", AuthenticatedAt: auth,
			MFAAt: auth.Add(time.Minute), ExpiresAt: auth.Add(20 * time.Minute),
		},
		Now: now, MaxSessionAge: 30 * time.Minute,
	}
}

func TestAuthorizeUseCaseGrantsViaPort(t *testing.T) {
	now := reignAnchor().Add(time.Hour)
	fake := &fakeReigns{current: reignSnapshot()}
	grant, err := NewAuthorizeUseCase(fake).Execute(context.Background(), reignCommand("rainha-1", 1, now))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if grant.Holder != "rainha-1" || grant.Reign != 1 {
		t.Fatalf("grant = %+v, want the invested office", grant)
	}
	if fake.calls != 1 {
		t.Fatalf("resolver calls = %d, want exactly one read per act", fake.calls)
	}
}

func TestAuthorizeUseCaseRefusesBeforeReading(t *testing.T) {
	now := reignAnchor().Add(time.Hour)
	fake := &fakeReigns{current: reignSnapshot()}
	cmd := reignCommand("", 1, now)
	if _, err := NewAuthorizeUseCase(fake).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrInvalidAuthority) {
		t.Fatalf("partial = %v, want ErrInvalidAuthority", err)
	}
	if fake.calls != 0 {
		t.Fatalf("resolver calls = %d, want zero: shapeless claims never reach storage", fake.calls)
	}
}

func TestAuthorizeUseCasePropagatesResolverFailure(t *testing.T) {
	now := reignAnchor().Add(time.Hour)
	boom := errors.New("reign store unavailable")
	fake := &fakeReigns{err: boom}
	if _, err := NewAuthorizeUseCase(fake).Execute(context.Background(), reignCommand("rainha-1", 1, now)); !errors.Is(err, boom) {
		t.Fatalf("failure = %v, want the store error untouched", err)
	}
}

func TestAuthorizeUseCaseRevokesOldHolder(t *testing.T) {
	now := reignAnchor().Add(time.Hour)
	anchor := reignAnchor()
	moved := domain.CurrentReign{
		Season: "temporada-1", Holder: "rainha-2", Reign: 2,
		StartsAt: anchor, EndsAt: anchor.Add(7776000 * time.Second), Open: true,
	}
	fake := &fakeReigns{current: moved}
	if _, err := NewAuthorizeUseCase(fake).Execute(context.Background(), reignCommand("rainha-1", 1, now)); !errors.Is(err, domain.ErrStaleReign) {
		t.Fatalf("old reign = %v, want ErrStaleReign", err)
	}
	grant, err := NewAuthorizeUseCase(fake).Execute(context.Background(), reignCommand("rainha-2", 2, now))
	if err != nil {
		t.Fatalf("new holder: %v", err)
	}
	if grant.Holder != "rainha-2" || grant.Reign != 2 {
		t.Fatalf("grant = %+v, want the new investiture", grant)
	}
}

func TestAuthorizeUseCaseRefusesOperatorKing(t *testing.T) {
	now := reignAnchor().Add(time.Hour)
	fake := &fakeReigns{current: domain.CurrentReign{
		Season: "temporada-1", Holder: "admin-1", Reign: 1,
		StartsAt: reignAnchor(), EndsAt: reignAnchor().Add(7776000 * time.Second), Open: true,
	}}
	cmd := reignCommand("admin-1", 1, now)
	cmd.Session.Subject = "admin-1"
	cmd.Operator = true
	if _, err := NewAuthorizeUseCase(fake).Execute(context.Background(), cmd); !errors.Is(err, domain.ErrOperatorNotSovereign) {
		t.Fatalf("operator = %v, want ErrOperatorNotSovereign", err)
	}
}
