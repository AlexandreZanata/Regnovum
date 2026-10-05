package application

import (
	"context"
	"strings"

	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

// ExportedLeader is one redacted co-leader: pseudonym display plus
// exact wealth only with explicit permission.
type ExportedLeader struct {
	Subject      string
	Display      string
	Wealth       int64
	WealthHidden bool
}

// ChampionsView is the privacy-safe history answer of one book: the
// cutoff identity, the co-leaders redacted by permission, the last
// King display and the locale titles. Raw wealth and real names
// never leave without permission; retention, hold and export are
// enforced by the store (P42) before this view is built.
type ChampionsView struct {
	Season         string
	CutoffRevision int64
	Hash           string
	Version        int
	Leaders        []ExportedLeader
	LastKing       string
	RichestTitle   string
	LastKingTitle  string
	HistoryTitle   string
}

// ChampionStore persists sealed champion archives derived strictly
// from cutoff snapshots: one archive per book with hash/version.
// Retention holds and export denials refuse in the store with a
// domain error, failing closed before any view is built.
type ChampionStore interface {
	FindArchive(ctx context.Context, season string) (seasondomain.ChampionArchive, bool, error)
}

// ChampionsUseCase serves allowlisted champion history through the
// archive port only. It wires nothing: fakes stand in for the store
// until the release gate.
type ChampionsUseCase struct {
	archives ChampionStore
}

// NewChampionsUseCase creates an instance of ChampionsUseCase.
func NewChampionsUseCase(archives ChampionStore) *ChampionsUseCase {
	return &ChampionsUseCase{archives: archives}
}

// GetChampions resolves the redacted champions of one allowlisted
// book for one authenticated account in one locale.
func (uc *ChampionsUseCase) GetChampions(ctx context.Context, accountID, seasonKey, locale string, omitNames, canSeeExact bool) (ChampionsView, error) {
	if uc.archives == nil {
		return ChampionsView{}, seasondomain.ErrInvalidSeason
	}
	if accountID == "" || strings.TrimSpace(accountID) != accountID {
		return ChampionsView{}, seasondomain.ErrInvalidSeason
	}
	if seasonKey == "" || strings.TrimSpace(seasonKey) != seasonKey {
		return ChampionsView{}, seasondomain.ErrInvalidSeason
	}
	if seasonKey == "compat-legacy" {
		return ChampionsView{}, seasondomain.ErrSeasonMismatch
	}
	parsedLocale, err := seasondomain.ParseSeasonLocale(locale)
	if err != nil {
		return ChampionsView{}, err
	}
	archive, found, err := uc.archives.FindArchive(ctx, seasonKey)
	if err != nil {
		return ChampionsView{}, err
	}
	if !found {
		return ChampionsView{}, seasondomain.ErrSeasonUnknown
	}
	if archive.Season != seasonKey {
		return ChampionsView{}, seasondomain.ErrInvalidSeason
	}
	exported := seasondomain.ExportChampions(archive, omitNames, canSeeExact)
	leaders := make([]ExportedLeader, 0, len(exported))
	for _, e := range exported {
		leaders = append(leaders, ExportedLeader{
			Subject: e.Subject, Display: e.Display,
			Wealth: e.Wealth, WealthHidden: e.WealthHidden,
		})
	}
	titles := seasondomain.ChampionTitlesFor(parsedLocale)
	lastKing := archive.LastKing
	if omitNames && lastKing != "" {
		lastKing = "alias-reservado"
	}
	return ChampionsView{
		Season: archive.Season, CutoffRevision: archive.CutoffRevision,
		Hash: archive.Hash, Version: archive.Version,
		Leaders: leaders, LastKing: lastKing,
		RichestTitle: titles.Richest, LastKingTitle: titles.LastKing, HistoryTitle: titles.History,
	}, nil
}
