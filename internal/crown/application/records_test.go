package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

type fakeBook struct {
	entries map[domain.ActID]domain.PublicRecord
	order   []domain.ActID
}

func (f *fakeBook) Append(_ context.Context, record domain.PublicRecord) error {
	book := domain.RoyalBook{}
	for _, id := range f.order {
		if err := book.Append(f.entries[id]); err != nil {
			return err
		}
	}
	if err := book.Append(record); err != nil {
		return err
	}
	if f.entries == nil {
		f.entries = map[domain.ActID]domain.PublicRecord{}
	}
	if _, ok := f.entries[record.Act]; !ok {
		f.order = append(f.order, record.Act)
	}
	f.entries[record.Act] = record
	return nil
}

func (f *fakeBook) Find(_ context.Context, act domain.ActID) (domain.PublicRecord, error) {
	record, ok := f.entries[act]
	if !ok {
		return domain.PublicRecord{}, domain.ErrUnrecordedAct
	}
	return record, nil
}

func (f *fakeBook) List(_ context.Context) ([]domain.PublicRecord, error) {
	out := make([]domain.PublicRecord, 0, len(f.order))
	for _, id := range f.order {
		out = append(out, f.entries[id])
	}
	return out, nil
}

func publishAnchor() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func publishAct(id string) domain.RoyalAct {
	anchor := publishAnchor()
	return domain.RoyalAct{
		ID: domain.ActID(id), Author: "rainha-1", Season: "temporada-1",
		Reign: 2, Competence: "patrimonial", Kind: domain.ActEconomic,
		Reason: "reparacao devida a ana sem expor a vitima",
		Target: "conta/ana", Effect: "transferir 1000 de tesouro-livre sem mint",
		DecreedAt: anchor, Effective: anchor.Add(time.Hour),
		EndsAt:  anchor.Add(30 * 24 * time.Hour),
		Charter: "v3", Origin: "tesouro-livre", Amount: 1000,
	}
}

func TestPublishUseCaseRecordsThroughPortOnly(t *testing.T) {
	store := &fakeBook{}
	record, err := NewPublishUseCase(store).Execute(context.Background(), PublishCommand{Act: publishAct("decreto-30")})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if record.Act != "decreto-30" || record.Author != "rainha-1" || record.Reign != 2 {
		t.Fatalf("record = %+v, want the attributable summary", record)
	}
	stored, err := store.Find(context.Background(), "decreto-30")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if stored.Digest != record.Digest {
		t.Fatalf("digest = %q vs %q, want ledger and book citing one seal", stored.Digest, record.Digest)
	}
	for _, secret := range []string{"ana", "transferir 1000", "tesouro-livre"} {
		if strings.Contains(record.Canonical(), secret) {
			t.Fatalf("public leaks %q: victims never appear in public", secret)
		}
	}
}

func TestPublishUseCaseReplaysWithoutDuplicating(t *testing.T) {
	store := &fakeBook{}
	uc := NewPublishUseCase(store)
	first, err := uc.Execute(context.Background(), PublishCommand{Act: publishAct("decreto-30")})
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	second, err := uc.Execute(context.Background(), PublishCommand{Act: publishAct("decreto-30")})
	if err != nil {
		t.Fatalf("replay Execute: %v", err)
	}
	if first != second || len(store.order) != 1 {
		t.Fatalf("replay duplicates: %+v vs %+v in %d entries", first, second, len(store.order))
	}
}

func TestPublishUseCaseRefusesAnonymousAndDetached(t *testing.T) {
	store := &fakeBook{}
	uc := NewPublishUseCase(store)
	ghost := publishAct("decreto-30")
	ghost.Author = ""
	if _, err := uc.Execute(context.Background(), PublishCommand{Act: ghost}); err == nil {
		t.Fatal("anonymous passed: logs without authorship record nothing")
	}
	correction := publishAct("decreto-31")
	correction.Corrects = "decreto-30"
	if _, err := uc.Execute(context.Background(), PublishCommand{Act: correction}); !errors.Is(err, domain.ErrDetachedCorrection) {
		t.Fatalf("detached = %v, want ErrDetachedCorrection", err)
	}
	if _, err := uc.Execute(context.Background(), PublishCommand{Act: publishAct("decreto-30")}); err != nil {
		t.Fatalf("original Execute: %v", err)
	}
	if _, err := uc.Execute(context.Background(), PublishCommand{Act: correction}); err != nil {
		t.Fatalf("linked correction: %v", err)
	}
}

func TestPublishUseCaseProvesCoverage(t *testing.T) {
	store := &fakeBook{}
	uc := NewPublishUseCase(store)
	if err := uc.VerifyCoverage(context.Background(), []domain.ActID{"decreto-30"}); !errors.Is(err, domain.ErrUnrecordedAct) {
		t.Fatalf("hidden = %v, want ErrUnrecordedAct", err)
	}
	if _, err := uc.Execute(context.Background(), PublishCommand{Act: publishAct("decreto-30")}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if err := uc.VerifyCoverage(context.Background(), []domain.ActID{"decreto-30"}); err != nil {
		t.Fatalf("covered: %v", err)
	}
}
