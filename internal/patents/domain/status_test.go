package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func patentAnchor() time.Time {
	return time.Date(2026, time.November, 4, 10, 0, 0, 0, time.UTC)
}

func patentSeason() (start, end time.Time) {
	start = time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)
	return start, start.Add(90 * 24 * time.Hour)
}

func patentTerms() *ApprovedTerms {
	return &ApprovedTerms{
		Version:         "patente-v1",
		PriceMinorUnits: 5000,
		Currency:        "BRL",
		Seats:           100,
		Duration:        30 * 24 * time.Hour,
		CosmeticGrant:   "filete dourado no perfil",
	}
}

func patentRequest() GrantRequest {
	start, end := patentSeason()
	return GrantRequest{
		ID: "patente-1", Holder: "ana", Season: "temporada-1",
		Title:       "filete dourado no perfil",
		GrantedAt:   patentAnchor(),
		SeasonStart: start, SeasonEnd: end,
		Terms: patentTerms(),
	}
}

func grantPatent(t *testing.T, req GrantRequest) PatentGrant {
	t.Helper()
	grant, err := GrantPatent(req)
	if err != nil {
		t.Fatalf("GrantPatent: %v", err)
	}
	return grant
}

func TestHonorificGrantLivesInsideItsSeason(t *testing.T) {
	grant := grantPatent(t, patentRequest())
	if grant.Season != "temporada-1" || grant.Holder != "ana" {
		t.Fatalf("grant = %+v, want holder and season identified", grant)
	}
	if grant.TermsVersion != "patente-v1" {
		t.Fatalf("grant = %+v, want the ratified terms version bound", grant)
	}
	if !grant.ExpiresAt.After(grant.GrantedAt) {
		t.Fatalf("grant = %+v, want a live window", grant)
	}
	_, seasonEnd := patentSeason()
	if !grant.ExpiresAt.Before(seasonEnd) && !grant.ExpiresAt.Equal(seasonEnd) {
		t.Fatalf("grant = %+v, want expiry capped at season end", grant)
	}
	if !grant.IsActive(patentAnchor()) {
		t.Fatalf("grant should be active at the sale instant")
	}
	if grant.IsActive(grant.ExpiresAt) {
		t.Fatalf("grant should be expired at the exact expiry tick")
	}
	honor := RememberHonor(grant)
	if honor.Holder != "ana" || honor.Season != "temporada-1" {
		t.Fatalf("honor = %+v, want the past honor kept as named history", honor)
	}
}

func TestMissingTermsBlockTheSale(t *testing.T) {
	base := patentRequest()
	if _, err := GrantPatent(GrantRequest{
		ID: base.ID, Holder: base.Holder, Season: base.Season, Title: base.Title,
		GrantedAt: base.GrantedAt, SeasonStart: base.SeasonStart, SeasonEnd: base.SeasonEnd,
	}); !errors.Is(err, ErrTermsMissing) {
		t.Fatalf("nil terms = %v, want ErrTermsMissing", err)
	}
	missing := []func(*ApprovedTerms){
		func(terms *ApprovedTerms) { terms.Version = "" },
		func(terms *ApprovedTerms) { terms.Currency = "" },
		func(terms *ApprovedTerms) { terms.CosmeticGrant = "" },
		func(terms *ApprovedTerms) { terms.PriceMinorUnits = 0 },
		func(terms *ApprovedTerms) { terms.Seats = 0 },
		func(terms *ApprovedTerms) { terms.Duration = 0 },
	}
	for i, spoil := range missing {
		terms := patentTerms()
		spoil(terms)
		req := patentRequest()
		req.ID = "patente-missing"
		req.Terms = terms
		if _, err := GrantPatent(req); !errors.Is(err, ErrTermsMissing) {
			t.Fatalf("missing term %d = %v, want ErrTermsMissing", i, err)
		}
	}
	malformed := patentRequest()
	malformed.Holder = " ana"
	if _, err := GrantPatent(malformed); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("blank holder = %v, want ErrInvalidGrant", err)
	}
	zero := patentRequest()
	zero.GrantedAt = time.Time{}
	if _, err := GrantPatent(zero); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("zero instant = %v, want ErrInvalidGrant", err)
	}
}

func TestPatentNeverTransfersOrRenews(t *testing.T) {
	grant := grantPatent(t, patentRequest())
	if err := TransferPatent(TransferRequest{Grant: grant, To: "bruno"}); !errors.Is(err, ErrNonTransferable) {
		t.Fatalf("transfer = %v, want ErrNonTransferable", err)
	}
	if err := TransferPatent(TransferRequest{Grant: grant, To: " mal"}); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("malformed recipient = %v, want ErrInvalidGrant", err)
	}
	honor := RememberHonor(grant)
	if err := RequireNewGrant(RenewRequest{Honor: honor, NewSeason: "temporada-2"}); !errors.Is(err, ErrNewGrantRequired) {
		t.Fatalf("renewal = %v, want ErrNewGrantRequired", err)
	}
	same := RenewRequest{Honor: honor, NewSeason: "temporada-1"}
	if err := RequireNewGrant(same); !errors.Is(err, ErrNewGrantRequired) {
		t.Fatalf("same-season renewal = %v, want ErrNewGrantRequired", err)
	}
}

func TestPatentGrantsNoOfficeOrRanking(t *testing.T) {
	grant := grantPatent(t, patentRequest())
	if grant.AuthorizesOffice() {
		t.Fatalf("patent must never authorize an office API")
	}
	if grant.RankingWeight() != 0 {
		t.Fatalf("patent must never move a factual ranking")
	}
	banned := []string{"Authority", "Autoridade", "Vote", "Voto", "Truth", "Verdade", "Reputation", "Reputacao", "Score", "Nota", "Wealth", "Riqueza", "Office", "Cargo", "Power", "Poder", "Payment", "Pagamento", "Balance", "Saldo", "Mint", "Treasury", "Tesouro"}
	for _, value := range []any{PatentGrant{}, HistoricHonor{}} {
		for i := 0; i < reflect.TypeOf(value).NumField(); i++ {
			field := reflect.TypeOf(value).Field(i).Name
			for _, deny := range banned {
				if strings.Contains(field, deny) {
					t.Fatalf("%T carries %s: honor buys no power", value, field)
				}
			}
		}
	}
	// ApprovedTerms carries the ratified price and seats on purpose:
	// the sale decision arrives there, never as a power of the grant.
	// It still must not smuggle authority, vote, truth or reputation.
	termsBanned := []string{"Authority", "Autoridade", "Vote", "Voto", "Truth", "Verdade", "Reputation", "Reputacao", "Office", "Cargo"}
	for i := 0; i < reflect.TypeOf(ApprovedTerms{}).NumField(); i++ {
		field := reflect.TypeOf(ApprovedTerms{}).Field(i).Name
		for _, deny := range termsBanned {
			if strings.Contains(field, deny) {
				t.Fatalf("%T carries %s: terms price no power", ApprovedTerms{}, field)
			}
		}
	}
}

func TestPatentWindowTicksAndSeasonCap(t *testing.T) {
	grant := grantPatent(t, patentRequest())
	before := grant.GrantedAt.Add(-time.Nanosecond)
	if grant.IsActive(before) {
		t.Fatalf("grant should not be active one tick before the sale")
	}
	if !grant.IsActive(grant.ExpiresAt.Add(-time.Nanosecond)) {
		t.Fatalf("grant should be active one tick before expiry")
	}
	if grant.IsActive(grant.ExpiresAt.Add(time.Nanosecond)) {
		t.Fatalf("grant should be expired one tick after expiry")
	}
	start, seasonEnd := patentSeason()
	late := patentRequest()
	late.ID = "patente-tardia"
	late.GrantedAt = seasonEnd.Add(-time.Hour)
	late.Terms = &ApprovedTerms{
		Version: "patente-v1", PriceMinorUnits: 5000, Currency: "BRL",
		Seats: 100, Duration: 30 * 24 * time.Hour, CosmeticGrant: "filete dourado no perfil",
	}
	capped, err := GrantPatent(late)
	if err != nil {
		t.Fatalf("GrantPatent near season end: %v", err)
	}
	if !capped.ExpiresAt.Equal(seasonEnd) {
		t.Fatalf("capped = %+v, want expiry limited to season end", capped)
	}
	closed := patentRequest()
	closed.ID = "patente-fechada"
	closed.GrantedAt = seasonEnd
	if _, err := GrantPatent(closed); !errors.Is(err, ErrExpired) {
		t.Fatalf("sale at season end = %v, want ErrExpired", err)
	}
	early := patentRequest()
	early.ID = "patente-precoce"
	early.GrantedAt = start.Add(-time.Nanosecond)
	if _, err := GrantPatent(early); !errors.Is(err, ErrExpired) {
		t.Fatalf("sale before season start = %v, want ErrExpired", err)
	}
}
