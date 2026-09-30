package domain

import (
	"errors"
	"testing"
	"time"
)

const (
	charterHashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	charterHashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func charterGenesis() time.Time {
	return time.Date(2026, time.January, 4, 0, 0, 0, 0, time.UTC)
}

func mustPublish(t *testing.T, chain []Release, req PublishRequest) ([]Release, Release) {
	t.Helper()
	release, err := Publish(chain, req)
	if err != nil {
		t.Fatalf("Publish(%+v): %v", req, err)
	}
	if err := release.VerifyHash(); err != nil {
		t.Fatalf("VerifyHash: %v", err)
	}
	return append(chain, release), release
}

func charterTwoVersions(t *testing.T) []Release {
	t.Helper()
	genesis := charterGenesis()
	var chain []Release
	var first Release
	chain, first = mustPublish(t, chain, PublishRequest{
		Version: "v1", Locale: "pt", EffectiveAt: genesis,
		ContentHash: charterHashA,
	})
	_ = first
	chain, _ = mustPublish(t, chain, PublishRequest{
		Version: "v2", Locale: "pt", EffectiveAt: genesis.AddDate(0, 0, 7),
		Previous: "v1", ContentHash: charterHashB,
	})
	return chain
}

func TestPublishSealsImmutableChain(t *testing.T) {
	genesis := charterGenesis()
	chain := charterTwoVersions(t)
	if len(chain) != 2 || chain[0].Previous != "" || chain[1].Previous != "v1" {
		t.Fatalf("chain = %+v, want genesis linked to v2", chain)
	}
	tampered := chain[1]
	tampered.ContentHash = charterHashA
	if err := tampered.VerifyHash(); !errors.Is(err, ErrInvalidCharter) {
		t.Fatalf("tampered VerifyHash = %v, want ErrInvalidCharter", err)
	}
	dup, err := Publish(chain, PublishRequest{
		Version: "v2", Locale: "pt", EffectiveAt: genesis.AddDate(0, 0, 14),
		Previous: "v1", ContentHash: charterHashB,
	})
	if !errors.Is(err, ErrInvalidCharter) || dup != (Release{}) {
		t.Fatalf("duplicate = %+v/%v, want refusal without a release", dup, err)
	}
	if _, err := Publish(chain, PublishRequest{
		Version: "v3", Locale: "pt", EffectiveAt: genesis.AddDate(0, 0, 14),
		Previous: "v1", ContentHash: charterHashA,
	}); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("skipped previous = %v, want ErrUnknownVersion: missing links block", err)
	}
	if _, err := Publish(chain, PublishRequest{
		Version: "v3", Locale: "pt", EffectiveAt: genesis,
		Previous: "v2", ContentHash: charterHashA,
	}); !errors.Is(err, ErrInvalidCharter) {
		t.Fatalf("non-increasing effective = %v, want ErrInvalidCharter", err)
	}
	for _, raw := range []PublishRequest{
		{Version: "1", Locale: "pt", EffectiveAt: genesis, ContentHash: charterHashA},
		{Version: "v0", Locale: "pt", EffectiveAt: genesis, ContentHash: charterHashA},
		{Version: "v01", Locale: "pt", EffectiveAt: genesis, ContentHash: charterHashA},
		{Version: "v1", Locale: "PT", EffectiveAt: genesis, ContentHash: charterHashA},
		{Version: "v1", Locale: "pt", EffectiveAt: genesis, ContentHash: "short"},
		{Version: "v1", Locale: "pt", ContentHash: charterHashA},
	} {
		if _, err := Publish(nil, raw); !errors.Is(err, ErrInvalidCharter) {
			t.Fatalf("Publish(%+v) = %v, want ErrInvalidCharter", raw, err)
		}
	}
}

func TestOldContractsKeepTheirVersion(t *testing.T) {
	chain := charterTwoVersions(t)
	genesis := charterGenesis()
	old, err := ResolveSubstantive(chain, CharterLocalePortuguese, genesis.Add(time.Hour))
	if err != nil || old.Version != "v1" {
		t.Fatalf("prior fact = %v/%v, want v1", old.Version, err)
	}
	sealed, err := ResolveSubstantive(chain, CharterLocalePortuguese, genesis.AddDate(0, 0, 30))
	if err != nil || sealed.Version != "v2" {
		t.Fatalf("later fact = %v/%v, want v2", sealed.Version, err)
	}
	tick := genesis.AddDate(0, 0, 7)
	before, err := ResolveSubstantive(chain, CharterLocalePortuguese, tick.Add(-time.Nanosecond))
	if err != nil || before.Version != "v1" {
		t.Fatalf("tick-1ns = %v/%v, want v1: the window is [effective, next)", before.Version, err)
	}
	at, err := ResolveSubstantive(chain, CharterLocalePortuguese, tick)
	if err != nil || at.Version != "v2" {
		t.Fatalf("exact tick = %v/%v, want v2", at.Version, err)
	}
}

func TestMissingVersionBlocksInsteadOfInferring(t *testing.T) {
	if _, err := VersionInForce(nil, CharterLocalePortuguese, charterGenesis()); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("empty chain = %v, want ErrUnknownVersion", err)
	}
	chain := charterTwoVersions(t)
	if _, err := VersionInForce(chain, CharterLocaleEnglish, charterGenesis().AddDate(0, 0, 30)); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("unpublished locale = %v, want ErrUnknownVersion: locales never infer each other", err)
	}
	if _, err := VersionInForce(chain, CharterLocalePortuguese, charterGenesis().Add(-time.Hour)); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("pre-genesis instant = %v, want ErrUnknownVersion", err)
	}
}
