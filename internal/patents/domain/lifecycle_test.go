package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func acquirePatent(t *testing.T, req GrantRequest) Holding {
	t.Helper()
	holding, err := AcquireHolding(AcquireRequest{Grant: req, Accepted: true})
	if err != nil {
		t.Fatalf("AcquireHolding: %v", err)
	}
	return holding
}

func TestVoluntaryAcquisitionGrantsOneLiveHolding(t *testing.T) {
	req := patentRequest()
	if _, err := AcquireHolding(AcquireRequest{Grant: req}); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("silent acquisition = %v, want ErrConsentRequired", err)
	}
	holding := acquirePatent(t, req)
	if err := CheckUse(UseRequest{Holding: holding, At: patentAnchor()}); err != nil {
		t.Fatalf("CheckUse: %v", err)
	}
	if !holding.IsUsable(patentAnchor()) {
		t.Fatalf("holding should be usable at the sale instant")
	}
	replay, err := AcquireSecond(holding, req)
	if err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if replay.Grant.ID != holding.Grant.ID || replay.Revoked || replay.DeadClosed || replay.Refunded {
		t.Fatalf("replay = %+v, want the same holding without new facts", replay)
	}
	if holding.Grant.AuthorizesOffice() || holding.Grant.RankingWeight() != 0 {
		t.Fatalf("a held patent must still grant no office and no ranking")
	}
}

func TestDuplicatePurchaseNeverDuplicates(t *testing.T) {
	holding := acquirePatent(t, patentRequest())
	spoils := []func(*GrantRequest){
		func(req *GrantRequest) { req.ID = "patente-2" },
		func(req *GrantRequest) { req.Title = "filete prateado no perfil" },
		func(req *GrantRequest) { req.GrantedAt = patentAnchor().Add(time.Minute) },
		func(req *GrantRequest) {
			terms := patentTerms()
			terms.Version = "patente-v2"
			req.Terms = terms
		},
	}
	for i, spoil := range spoils {
		req := patentRequest()
		spoil(&req)
		if _, err := AcquireSecond(holding, req); !errors.Is(err, ErrDuplicateHold) {
			t.Fatalf("second purchase %d = %v, want ErrDuplicateHold", i, err)
		}
	}
	other := patentRequest()
	other.ID = "patente-bruno"
	other.Holder = "bruno"
	stranger, err := AcquireHolding(AcquireRequest{Grant: other, Accepted: true})
	if err != nil {
		t.Fatalf("another holder acquires its own holding: %v", err)
	}
	if stranger.Grant.Holder != "bruno" {
		t.Fatalf("stranger = %+v, want holdings kept per holder", stranger)
	}
}

func TestSeasonLimitAndExpiryBoundUse(t *testing.T) {
	holding := acquirePatent(t, patentRequest())
	if err := CheckUse(UseRequest{Holding: holding, At: holding.Grant.ExpiresAt}); !errors.Is(err, ErrExpired) {
		t.Fatalf("use at expiry tick = %v, want ErrExpired", err)
	}
	if holding.IsUsable(holding.Grant.ExpiresAt.Add(time.Nanosecond)) {
		t.Fatalf("holding should stay expired one tick after expiry")
	}
	_, seasonEnd := patentSeason()
	if err := CheckUse(UseRequest{Holding: holding, At: seasonEnd}); !errors.Is(err, ErrExpired) {
		t.Fatalf("use past season end = %v, want ErrExpired", err)
	}
	if err := RequireNewGrant(RenewRequest{
		Honor:     RememberHonor(holding.Grant),
		NewSeason: "temporada-2",
	}); !errors.Is(err, ErrNewGrantRequired) {
		t.Fatalf("new season without a new sale must refuse")
	}
	fresh := patentRequest()
	fresh.ID = "patente-2"
	fresh.Season = "temporada-2"
	fresh.GrantedAt = seasonEnd.Add(time.Hour)
	fresh.SeasonStart = seasonEnd
	fresh.SeasonEnd = seasonEnd.Add(90 * 24 * time.Hour)
	renewed, err := AcquireHolding(AcquireRequest{Grant: fresh, Accepted: true})
	if err != nil {
		t.Fatalf("new season sale with acceptance: %v", err)
	}
	if !renewed.IsUsable(fresh.GrantedAt) {
		t.Fatalf("renewed = %+v, want the new book usable on its own sale", renewed)
	}
	if holding.IsUsable(fresh.GrantedAt) {
		t.Fatalf("old holding must never become usable in the new season")
	}
}

func TestRevocationEndsUseWithRule(t *testing.T) {
	holding := acquirePatent(t, patentRequest())
	at := patentAnchor().Add(time.Hour)
	revoked, err := RevokeHolding(RevokeRequest{
		Holding: holding, At: at,
		Reason: "selo adulterado na compra", Rule: "regra-patente-3",
	})
	if err != nil {
		t.Fatalf("RevokeHolding: %v", err)
	}
	if err := CheckUse(UseRequest{Holding: revoked, At: at}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("use after revoke = %v, want ErrRevoked", err)
	}
	replay, err := RevokeHolding(RevokeRequest{
		Holding: revoked, At: at,
		Reason: "selo adulterado na compra", Rule: "regra-patente-3",
	})
	if err != nil {
		t.Fatalf("identical revoke replay: %v", err)
	}
	if !replay.Revoked || replay.RevokeRule != "regra-patente-3" {
		t.Fatalf("replay = %+v, want the same revocation fact", replay)
	}
	if _, err := RevokeHolding(RevokeRequest{
		Holding: revoked, At: at.Add(time.Hour),
		Reason: "outro motivo", Rule: "regra-patente-3",
	}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("second revoke = %v, want ErrRevoked", err)
	}
	for _, bad := range []RevokeRequest{
		{Holding: holding, At: at},
		{Holding: holding, At: at, Reason: "selo adulterado na compra"},
	} {
		if _, err := RevokeHolding(bad); !errors.Is(err, ErrInvalidGrant) {
			t.Fatalf("reasonless revoke = %v, want ErrInvalidGrant", err)
		}
	}
	expired, err := RevokeHolding(RevokeRequest{
		Holding: holding, At: holding.Grant.ExpiresAt,
		Reason: "selo adulterado na compra", Rule: "regra-patente-3",
	})
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("revoke past expiry = %v, want ErrExpired (holding %v)", err, expired)
	}
}

func TestDeathClosesAndBlessingNeverReopens(t *testing.T) {
	holding := acquirePatent(t, patentRequest())
	closed, err := CloseForDeath(holding, patentAnchor().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("CloseForDeath: %v", err)
	}
	if err := CheckUse(UseRequest{Holding: closed, At: patentAnchor().Add(3 * time.Hour)}); !errors.Is(err, ErrDeathClosed) {
		t.Fatalf("use after death = %v, want ErrDeathClosed", err)
	}
	blessed := UseRequest{Holding: closed, At: patentAnchor().Add(3 * time.Hour), Blessed: true}
	if err := CheckUse(blessed); !errors.Is(err, ErrDeathClosed) {
		t.Fatalf("blessed use = %v, want ErrDeathClosed: blessing restores participation, never the patent", err)
	}
	again, err := CloseForDeath(closed, patentAnchor().Add(4*time.Hour))
	if err != nil {
		t.Fatalf("second CloseForDeath: %v", err)
	}
	if !again.ClosedAt.Equal(closed.ClosedAt) {
		t.Fatalf("again = %+v, want the first instant to win", again)
	}
	if _, err := CloseForDeath(holding, time.Time{}); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("zero instant close = %v, want ErrInvalidGrant", err)
	}
}

func TestServiceFailureRefundsOnceInOriginBook(t *testing.T) {
	holding := acquirePatent(t, patentRequest())
	at := patentAnchor().Add(time.Hour)
	refundReq := RefundRequest{
		Holding: holding, At: at, Reason: ServiceFailureReason,
		ReceiptID: "recibo-1", AmountMinorUnits: 5000, Currency: "BRL",
		Treasury: TreasuryDestination, Book: "temporada-1",
	}
	refunded, err := RefundHolding(refundReq)
	if err != nil {
		t.Fatalf("RefundHolding: %v", err)
	}
	if refunded.RefundReceipt.Book != "temporada-1" || refunded.RefundReceipt.Treasury != TreasuryDestination {
		t.Fatalf("refunded = %+v, want the origin book with a treasury receipt", refunded)
	}
	if err := CheckUse(UseRequest{Holding: refunded, At: at}); !errors.Is(err, ErrRefundClosed) {
		t.Fatalf("use after refund = %v, want ErrRefundClosed", err)
	}
	replay, err := RefundHolding(refundReq)
	if err != nil {
		t.Fatalf("identical refund replay: %v", err)
	}
	if !replay.Refunded || replay.RefundReceipt.ID != "recibo-1" {
		t.Fatalf("replay = %+v, want the same single refund", replay)
	}
	divergent := refundReq
	divergent.Holding = refunded
	divergent.ReceiptID = "recibo-2"
	if _, err := RefundHolding(divergent); !errors.Is(err, ErrRefundClosed) {
		t.Fatalf("second refund = %v, want ErrRefundClosed", err)
	}
	foreign := refundReq
	foreign.Holding = acquirePatent(t, patentRequest())
	foreign.Book = "temporada-2"
	if _, err := RefundHolding(foreign); !errors.Is(err, ErrRefundClosed) {
		t.Fatalf("foreign-book refund = %v, want ErrRefundClosed", err)
	}
	banned := []string{"Mint", "Burn", "Balance", "Saldo", "Wealth", "Riqueza", "Office", "Cargo", "Vote", "Voto"}
	for _, value := range []any{Holding{}, RefundReceipt{}} {
		for i := 0; i < reflect.TypeOf(value).NumField(); i++ {
			field := reflect.TypeOf(value).Field(i).Name
			for _, deny := range banned {
				if strings.Contains(field, deny) {
					t.Fatalf("%T carries %s: restitution returns price, never mints", value, field)
				}
			}
		}
	}
}
