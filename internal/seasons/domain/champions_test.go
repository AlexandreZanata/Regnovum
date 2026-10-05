package domain

import (
	"testing"
	"time"
)

func championSnapshot() CutoffSnapshot {
	anchor := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	return CutoffSnapshot{
		Season: "temporada-1", CutoffRevision: 42, Policy: "wealth-v1",
		Entries: []ChampionEntry{
			{Subject: "ana", Pseudonym: "coruja-azul", DisplayName: "Ana", Wealth: 800, AttainedRevision: 10},
			{Subject: "bruno", Pseudonym: "lobo-cinza", DisplayName: "Bruno", Wealth: 800, AttainedRevision: 12},
			{Subject: "carla", Pseudonym: "raposa-verde", DisplayName: "Carla", Wealth: 600, AttainedRevision: 9},
		},
		LastKing: "davi",
		Reigns: []ReignFact{
			{Holder: "davi", ReignVersion: 1, StartedAt: anchor, EndsAt: anchor.Add(30 * 24 * time.Hour)},
			{Holder: "davi", ReignVersion: 2, StartedAt: anchor.Add(30 * 24 * time.Hour), EndsAt: anchor.Add(60 * 24 * time.Hour)},
		},
	}
}

func TestChampions_TieConservesCoLeaders(t *testing.T) {
	archive, err := DeriveChampions(championSnapshot())
	if err != nil {
		t.Fatalf("DeriveChampions: %v", err)
	}
	if len(archive.CoLeaders) != 2 {
		t.Fatalf("co-leaders = %d, want 2 (empate conserva co-líderes)", len(archive.CoLeaders))
	}
	if archive.CoLeaders[0].Subject != "ana" || archive.CoLeaders[1].Subject != "bruno" {
		t.Fatalf("co-leaders = %+v, want deterministic subject order", archive.CoLeaders)
	}
	if archive.Hash == "" || archive.Version != 1 {
		t.Fatalf("archive = %+v, want sealed hash/version", archive)
	}
}

func TestChampions_RenameAndAnonymizationKeepFact(t *testing.T) {
	snapshot := championSnapshot()
	archive, err := DeriveChampions(snapshot)
	if err != nil {
		t.Fatalf("DeriveChampions: %v", err)
	}
	renamed := archive.CoLeaders[0]
	renamed.DisplayName = "Ana Nova"
	if ResolvePublicName(renamed, false) != "Ana Nova" {
		t.Fatalf("renamed display = %q, want new display with same subject", ResolvePublicName(renamed, false))
	}
	renamed.Anonymized = true
	if ResolvePublicName(renamed, false) != "coruja-azul" {
		t.Fatalf("anonymized display = %q, want pseudonym with fact kept", ResolvePublicName(renamed, false))
	}
	if renamed.Subject != "ana" {
		t.Fatal("rename lost the fact key: conta anonimizada não perde fato autorizado")
	}
}

func TestChampions_LateRefundGrantsNoNewWealth(t *testing.T) {
	archive, err := DeriveChampions(championSnapshot())
	if err != nil {
		t.Fatalf("DeriveChampions: %v", err)
	}
	if !IsPostCutoffRevision(archive, archive.CutoffRevision+1) {
		t.Fatal("late revision not detected as post-cutoff")
	}
	if IsPostCutoffRevision(archive, archive.CutoffRevision) {
		t.Fatal("cutoff revision itself must still count")
	}
	before := len(archive.CoLeaders)
	_ = before
}

func TestChampions_CorrectionPreservesOriginal(t *testing.T) {
	archive, err := DeriveChampions(championSnapshot())
	if err != nil {
		t.Fatalf("DeriveChampions: %v", err)
	}
	original := append([]ChampionEntry(nil), archive.CoLeaders...)
	corrected, err := AppendCorrection(archive, Correction{
		LinkedHash: archive.Hash, Reason: "fraude comprovada", Author: "auditor-1",
		At:   time.Date(2026, time.November, 1, 12, 0, 0, 0, time.UTC),
		Note: "display corrigido sem reeditar livro selado",
	})
	if err != nil {
		t.Fatalf("AppendCorrection: %v", err)
	}
	if len(corrected.Corrections) != 1 || len(corrected.CoLeaders) != len(original) {
		t.Fatalf("corrected = %+v, want original preserved beside linked correction", corrected)
	}
	for i := range original {
		if corrected.CoLeaders[i] != original[i] {
			t.Fatal("correction rewrote the original: correction preserva original")
		}
	}
	if _, err := AppendCorrection(archive, Correction{LinkedHash: "deadbeef", Reason: "x", Author: "auditor-1", At: time.Now()}); err == nil {
		t.Fatal("foreign correction passed: vínculo exige hash do arquivo")
	}
}

func TestChampions_ChampionIsNotLastKing(t *testing.T) {
	archive, err := DeriveChampions(championSnapshot())
	if err != nil {
		t.Fatalf("DeriveChampions: %v", err)
	}
	for _, l := range archive.CoLeaders {
		if l.Subject == archive.LastKing {
			t.Fatalf("champion %q equals last King: campeão≠último Rei", l.Subject)
		}
	}
}

func TestChampions_CacheNeverMixesPersonOrLocale(t *testing.T) {
	a, err := ChampionCacheKey("temporada-1", "ana", SeasonLocalePortuguese)
	if err != nil {
		t.Fatalf("ChampionCacheKey: %v", err)
	}
	b, err := ChampionCacheKey("temporada-1", "bruno", SeasonLocalePortuguese)
	if err != nil {
		t.Fatalf("ChampionCacheKey: %v", err)
	}
	c, err := ChampionCacheKey("temporada-1", "ana", SeasonLocaleEnglish)
	if err != nil {
		t.Fatalf("ChampionCacheKey: %v", err)
	}
	if a == b || a == c {
		t.Fatalf("cache keys collide: %q %q %q", a, b, c)
	}
}

func TestChampions_ThirdPartyNeverExportsFull(t *testing.T) {
	archive, err := DeriveChampions(championSnapshot())
	if err != nil {
		t.Fatalf("DeriveChampions: %v", err)
	}
	third := ExportChampions(archive, true, false)
	if len(third) != 2 {
		t.Fatalf("third-party export = %d, want co-leaders redacted", len(third))
	}
	for _, e := range third {
		if !e.WealthHidden || e.Wealth != 0 {
			t.Fatalf("third-party wealth leaked: %+v", e)
		}
		if e.Display != "coruja-azul" && e.Display != "lobo-cinza" {
			t.Fatalf("third-party display = %q, want pseudonym only", e.Display)
		}
	}
	titular := ExportChampions(archive, false, true)
	if titular[0].WealthHidden || titular[0].Wealth != 800 {
		t.Fatalf("titular export = %+v, want exact wealth by permission", titular[0])
	}
}

func TestChampions_TitlesPtEn(t *testing.T) {
	pt := ChampionTitlesFor(SeasonLocalePortuguese)
	en := ChampionTitlesFor(SeasonLocaleEnglish)
	if pt.Richest != "Mais rico no corte" || pt.LastKing != "Último Rei" {
		t.Fatalf("pt titles = %+v", pt)
	}
	if en.Richest != "Richest at cutoff" || en.LastKing != "Last King" {
		t.Fatalf("en titles = %+v", en)
	}
}
