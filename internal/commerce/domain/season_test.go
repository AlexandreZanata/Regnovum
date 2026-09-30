package domain_test

import (
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
)

func TestCommerceSeasonWindowAndMatch(t *testing.T) {
	starts := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ends := starts.Add(7776000 * time.Second)
	if err := domain.CheckSeasonWindow(domain.SeasonKey("temporada-1"), ends, starts, ends); err == nil {
		t.Fatal("tick do fim admitiu: fim exclusivo pertence a sucessora")
	}
	if err := domain.CheckSeasonWindow(domain.SeasonKey("temporada-1"), ends.Add(-time.Nanosecond), starts, ends); err != nil {
		t.Fatalf("tick antes do fim recusou: %v", err)
	}
	if err := domain.CheckSeasonMatch(domain.SeasonKey("a"), domain.SeasonKey("b")); err == nil {
		t.Fatal("livros distintos casaram: mesma chave em outro livro nao redireciona")
	}
	if err := domain.RequireSeasonalReset(domain.SeasonKey("temporada-1"), false); err == nil {
		t.Fatal("sem reset autorizou: reset omitido nunca autoriza")
	}
	if _, err := domain.ParseSeasonKey("   "); err == nil {
		t.Fatal("chave em branco aceita: temporada ausente recusa antes de ler")
	}
	now := starts.Add(time.Hour)
	contract, err := domain.FundSeasonalContract(domain.SeasonalContractRequest{
		ContractRequest: domain.ContractRequest{
			Key: "k1", Object: "revisao", Buyer: "comprador-1", Provider: "prestador-1",
			AmountMill: 20000, ExpiresAt: ends.Add(-time.Hour), Now: now,
		},
		Season: domain.SeasonKey("temporada-1"), PolicyRef: "terminal-v1",
		PolicyHash:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		BuyerAccept: "aceite-comprador-1", ProviderAccept: "aceite-prestador-1",
		SeasonEndsAt: ends, ResetAcknowledged: true,
	})
	if err != nil {
		t.Fatalf("FundSeasonalContract: %v", err)
	}
	if string(contract.Season) != "temporada-1" {
		t.Fatalf("season = %q, want temporada-1", contract.Season)
	}
}
