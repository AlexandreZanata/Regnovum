package application

import (
	"context"
	"testing"
	"time"

	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

type fakeChampionStore struct {
	archives map[string]seasondomain.ChampionArchive
	err      error
}

func (f *fakeChampionStore) FindArchive(_ context.Context, season string) (seasondomain.ChampionArchive, bool, error) {
	if f.err != nil {
		return seasondomain.ChampionArchive{}, false, f.err
	}
	a, ok := f.archives[season]
	return a, ok, nil
}

func championArchive() seasondomain.ChampionArchive {
	anchor := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	archive, err := seasondomain.DeriveChampions(seasondomain.CutoffSnapshot{
		Season: "temporada-1", CutoffRevision: 42, Policy: "wealth-v1",
		Entries: []seasondomain.ChampionEntry{
			{Subject: "ana", Pseudonym: "coruja-azul", DisplayName: "Ana", Wealth: 800, AttainedRevision: 10},
			{Subject: "bruno", Pseudonym: "lobo-cinza", DisplayName: "Bruno", Wealth: 800, AttainedRevision: 12},
		},
		LastKing: "davi",
		Reigns: []seasondomain.ReignFact{
			{Holder: "davi", ReignVersion: 1, StartedAt: anchor, EndsAt: anchor.Add(30 * 24 * time.Hour)},
		},
	})
	if err != nil {
		panic(err)
	}
	return archive
}

func TestChampions_ThirdPartyRedactedAndLocaleTitled(t *testing.T) {
	uc := NewChampionsUseCase(&fakeChampionStore{archives: map[string]seasondomain.ChampionArchive{"temporada-1": championArchive()}})
	pt, err := uc.GetChampions(context.Background(), "conta-1", "temporada-1", "pt", true, false)
	if err != nil {
		t.Fatalf("GetChampions pt: %v", err)
	}
	if pt.RichestTitle != "Mais rico no corte" || len(pt.Leaders) != 2 {
		t.Fatalf("pt view = %+v", pt)
	}
	for _, l := range pt.Leaders {
		if !l.WealthHidden {
			t.Fatalf("third-party wealth leaked: %+v", l)
		}
	}
	en, err := uc.GetChampions(context.Background(), "conta-1", "temporada-1", "en", true, false)
	if err != nil {
		t.Fatalf("GetChampions en: %v", err)
	}
	if en.RichestTitle != "Richest at cutoff" {
		t.Fatalf("en view = %+v", en)
	}
	titular, err := uc.GetChampions(context.Background(), "ana", "temporada-1", "pt", false, true)
	if err != nil {
		t.Fatalf("GetChampions titular: %v", err)
	}
	if titular.Leaders[0].WealthHidden || titular.Leaders[0].Wealth != 800 {
		t.Fatalf("titular view = %+v, want exact wealth by permission", titular)
	}
}

func TestChampions_RetentionHoldFailsClosed(t *testing.T) {
	uc := NewChampionsUseCase(&fakeChampionStore{err: seasondomain.ErrSeasonClosed})
	if _, err := uc.GetChampions(context.Background(), "conta-1", "temporada-1", "pt", true, false); err == nil {
		t.Fatal("retention hold passed: retenção/hold respeita P42 e falha fechado")
	}
}
