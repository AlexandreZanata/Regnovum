package domain

import (
	"errors"
	"testing"
	"time"
)

func TestCorrectionBindsFutureFactsOnly(t *testing.T) {
	chain := charterTwoVersions(t)
	genesis := charterGenesis()
	now := genesis.AddDate(0, 0, 30)
	corrected, err := PublishCorrection(chain, PublishRequest{
		Version: "v3", Locale: "pt", EffectiveAt: now.Add(time.Hour),
		Previous: "v2", ContentHash: charterHashA,
	}, now)
	if err != nil {
		t.Fatalf("PublishCorrection: %v", err)
	}
	chain = append(chain, corrected)
	before, err := ResolveSubstantive(chain, CharterLocalePortuguese, now)
	if err != nil || before.Version != "v2" {
		t.Fatalf("pre-correction fact = %v/%v, want v2", before.Version, err)
	}
	after, err := ResolveSubstantive(chain, CharterLocalePortuguese, now.Add(2*time.Hour))
	if err != nil || after.Version != "v3" {
		t.Fatalf("post-correction fact = %v/%v, want v3", after.Version, err)
	}
	if _, err := PublishCorrection(chain, PublishRequest{
		Version: "v4", Locale: "pt", EffectiveAt: now.Add(-time.Hour),
		Previous: "v3", ContentHash: charterHashB,
	}, now); !errors.Is(err, ErrRetroactiveCorrection) {
		t.Fatalf("backdated correction = %v, want ErrRetroactiveCorrection", err)
	}
}

func TestHarmfulRetroactivityRefusesWhileCleanApplies(t *testing.T) {
	chain := charterTwoVersions(t)
	if _, err := ApplyProcedure(chain, CharterLocalePortuguese, "v1", "v2", true); !errors.Is(err, ErrHarmfulRetroactivity) {
		t.Fatalf("prejudicial new procedure = %v, want ErrHarmfulRetroactivity", err)
	}
	applied, err := ApplyProcedure(chain, CharterLocalePortuguese, "v1", "v2", false)
	if err != nil || applied.Version != "v2" {
		t.Fatalf("clean new procedure = %v/%v, want v2", applied.Version, err)
	}
	contemporary, err := ApplyProcedure(chain, CharterLocalePortuguese, "v2", "v1", true)
	if err != nil || contemporary.Version != "v2" {
		t.Fatalf("older procedure = %v/%v, want the contemporary v2 standing", contemporary.Version, err)
	}
	if _, err := ApplyProcedure(chain, CharterLocalePortuguese, "v1", "v9", false); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("missing procedure = %v, want ErrUnknownVersion", err)
	}
}
