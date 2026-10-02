package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func blessAnchor() time.Time {
	return time.Date(2026, time.February, 9, 10, 0, 0, 0, time.UTC)
}

func blessReign() CurrentReign {
	return CurrentReign{
		Season: "temporada-1", Holder: "rei-1", Reign: 2,
		StartsAt: blessAnchor(), EndsAt: blessAnchor().Add(90 * 24 * time.Hour),
		Open: true,
	}
}

func blessDeath() DeathMark {
	return DeathMark{
		Account: "ana", Sanction: "sentenca-1",
		Season: "temporada-1", Charter: "v2",
		DeadAt: blessAnchor().Add(time.Hour),
	}
}

func blessAct() RoyalAct {
	return RoyalAct{
		ID: "bencao-1", Author: "rei-1", Season: "temporada-1", Reign: 2,
		Competence: "bencao-real: reabrir participacao",
		Kind:       ActBlessing, Reason: "pena digital cumprida, retorno prospectivo",
		Target: "ana", Effect: "reabrir participacao e autenticacao por ato novo",
		DecreedAt: blessAnchor().Add(2 * time.Hour),
		Effective: blessAnchor().Add(3 * time.Hour),
		Charter:   "v2",
	}
}

func blessRequest() BlessRequest {
	return BlessRequest{
		Act: blessAct(), Death: blessDeath(), Current: blessReign(),
	}
}

func TestBlessingReopensOnlyAuthentication(t *testing.T) {
	blessing, err := GrantBlessing(blessRequest())
	if err != nil {
		t.Fatalf("GrantBlessing: %v", err)
	}
	if !blessing.CanAuthenticate || blessing.Account != "ana" {
		t.Fatalf("blessing = %+v, want authentication reopened for the account", blessing)
	}
	if blessing.Sanction != "sentenca-1" {
		t.Fatalf("blessing = %+v, want the sanction kept visible", blessing)
	}
	if blessing.Season != "temporada-1" || blessing.Reign != 2 || blessing.Charter != "v2" {
		t.Fatalf("blessing = %+v, want the current book, reign and charter", blessing)
	}
	if !blessing.BlessedAt.Equal(blessAnchor().Add(3 * time.Hour)) {
		t.Fatalf("blessing = %+v, want the prospective act effect", blessing)
	}
	old := Grant{
		Act: "sessao-1", Season: "temporada-1", Holder: "ana",
		Competence: "bencao-real: reabrir participacao", Reign: 1,
		ExpiresAt: blessAnchor().Add(4 * time.Hour),
	}
	if err := RevalidateGrant(old, blessReign(), blessAnchor().Add(3*time.Hour)); !errors.Is(err, ErrStaleReign) {
		t.Fatalf("old reign grant = %v, want ErrStaleReign", err)
	}
}

func TestBlessingRevivesWithoutMint(t *testing.T) {
	minted := blessRequest()
	minted.ClaimedWealth = 800000000000
	if _, err := GrantBlessing(minted); !errors.Is(err, ErrMintedBlessing) {
		t.Fatalf("claimed wealth = %v, want ErrMintedBlessing", err)
	}
	economic := blessRequest()
	economic.Act.Kind = ActEconomic
	economic.Act.Origin = "tesouro-livre"
	economic.Act.Amount = 1000
	if _, err := GrantBlessing(economic); !errors.Is(err, ErrMintedBlessing) {
		t.Fatalf("economic blessing = %v, want ErrMintedBlessing", err)
	}
	sealed := blessRequest()
	sealed.SealedWealth = 800000000000
	sealed.ClaimedWealth = 0
	blessing, err := GrantBlessing(sealed)
	if err != nil {
		t.Fatalf("sealed wealth standing: %v", err)
	}
	if blessing.Account != "ana" || !blessing.CanAuthenticate {
		t.Fatalf("blessing = %+v, want sealed wealth staying sealed with return intact", blessing)
	}
}

func TestBlessingKeepsThirdPartiesAndSanction(t *testing.T) {
	third := blessRequest()
	third.Act.Target = "terceiro-1"
	if _, err := GrantBlessing(third); !errors.Is(err, ErrInvalidBlessing) {
		t.Fatalf("third-party target = %v, want ErrInvalidBlessing", err)
	}
	self := blessRequest()
	self.Act.Author = "ana"
	if _, err := GrantBlessing(self); !errors.Is(err, ErrNotHolder) {
		t.Fatalf("non-holder blessing = %v, want ErrNotHolder", err)
	}
	alias := blessRequest()
	alias.Current.Holder = "ana"
	alias.Act.Author = "ana"
	if _, err := GrantBlessing(alias); !errors.Is(err, ErrInvalidBlessing) {
		t.Fatalf("self blessing = %v, want ErrInvalidBlessing", err)
	}
	reset := blessRequest()
	reset.Act = RoyalAct{}
	if _, err := GrantBlessing(reset); !errors.Is(err, ErrInvalidAuthority) {
		t.Fatalf("reset without act = %v, want ErrInvalidAuthority", err)
	}
	if reset.Death.Sanction != "sentenca-1" {
		t.Fatalf("death = %+v, want the sanction surviving reset alone", reset.Death)
	}
	replay := blessRequest()
	replay.Act.ID = "sentenca-1"
	if _, err := GrantBlessing(replay); !errors.Is(err, ErrInvalidBlessing) {
		t.Fatalf("sanction replay as blessing = %v, want ErrInvalidBlessing", err)
	}
}

func TestOldSessionAndReignStayInvalid(t *testing.T) {
	stale := blessRequest()
	stale.Act.Reign = 1
	if _, err := GrantBlessing(stale); !errors.Is(err, ErrStaleReign) {
		t.Fatalf("stale reign blessing = %v, want ErrStaleReign", err)
	}
	future := blessRequest()
	future.Act.Reign = 3
	future.Current.Reign = 2
	if _, err := GrantBlessing(future); !errors.Is(err, ErrFutureReign) {
		t.Fatalf("future reign blessing = %v, want ErrFutureReign", err)
	}
	foreign := blessRequest()
	foreign.Act.Season = "temporada-2"
	if _, err := GrantBlessing(foreign); !errors.Is(err, ErrSeasonMismatch) {
		t.Fatalf("foreign season blessing = %v, want ErrSeasonMismatch", err)
	}
	closed := blessRequest()
	closed.Current.Open = false
	if _, err := GrantBlessing(closed); !errors.Is(err, ErrSeasonClosed) {
		t.Fatalf("closed book blessing = %v, want ErrSeasonClosed", err)
	}
}

func TestCharterAcceptanceAndNoRecreatedAssets(t *testing.T) {
	changed := blessRequest()
	changed.Act.Charter = "v3"
	if _, err := GrantBlessing(changed); !errors.Is(err, ErrInvalidBlessing) {
		t.Fatalf("new charter without acceptance = %v, want ErrInvalidBlessing", err)
	}
	changed.CharterAccepted = true
	if _, err := GrantBlessing(changed); err != nil {
		t.Fatalf("accepted new charter: %v", err)
	}
	banned := []string{"Balance", "Saldo", "Wealth", "Riqueza", "Office", "Cargo", "Contract", "Contrato", "Patent", "Patente", "Amount", "Valor", "Mint", "Treasury", "Tesouro", "Vault", "Cofre", "Money", "Payment", "Pagamento"}
	value := Blessing{}
	for i := 0; i < reflect.TypeOf(value).NumField(); i++ {
		field := reflect.TypeOf(value).Field(i).Name
		for _, deny := range banned {
			if strings.Contains(field, deny) {
				t.Fatalf("Blessing carries %s: return never recreates assets", field)
			}
		}
	}
	office := blessRequest()
	office.Act.Kind = ActOffice
	if _, err := GrantBlessing(office); !errors.Is(err, ErrInvalidBlessing) {
		t.Fatalf("office kind as return = %v, want ErrInvalidBlessing", err)
	}
}
