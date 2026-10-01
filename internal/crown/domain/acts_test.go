package domain

import (
	"errors"
	"testing"
	"time"
)

func actAnchor() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func wholeAct() RoyalAct {
	anchor := actAnchor()
	return RoyalAct{
		ID: "decreto-1", Author: "rainha-1", Season: "temporada-1",
		Reign: 1, Competence: "cerimonial", Kind: ActOffice,
		Reason: "reordenar a corte para a temporada",
		Target: "corte/temporada-1", Effect: "nomear dois arautos sem tocar saldo",
		DecreedAt: anchor, Effective: anchor.Add(time.Hour),
		EndsAt:  anchor.Add(30 * 24 * time.Hour),
		Charter: "v2",
	}
}

func TestParseActKindIsClosed(t *testing.T) {
	for _, kind := range []string{
		"normativo", "conta-cargo-status", "processo", "economico", "perdao", "bencao",
	} {
		if _, err := ParseActKind(kind); err != nil {
			t.Fatalf("kind %q: %v", kind, err)
		}
	}
	for _, raw := range []string{
		"", "Normativo", " economico", "alterar-prazo", "alterar-genesis",
		"alterar-riqueza", "mudar-reset", "mudar-limiar", "dar-tesouro",
	} {
		if _, err := ParseActKind(raw); !errors.Is(err, ErrUnknownAct) {
			t.Fatalf("kind %q = %v, want ErrUnknownAct: season alterations never qualify", raw, err)
		}
	}
}

func TestDefineActSealsWholeDecree(t *testing.T) {
	act, err := DefineAct(wholeAct())
	if err != nil {
		t.Fatalf("DefineAct: %v", err)
	}
	if act.Kind != ActOffice || act.Charter != "v2" || act.Amount != 0 {
		t.Fatalf("sealed = %+v, want the explicit versioned act", act)
	}
	if !act.DecreedAt.Equal(actAnchor()) || !act.Effective.After(act.DecreedAt) {
		t.Fatalf("vigour = %v -> %v, want prospective window", act.DecreedAt, act.Effective)
	}
}

func TestDefineActRefusesIncomplete(t *testing.T) {
	base := wholeAct()
	for name, mutate := range map[string]func(*RoyalAct){
		"blank reason":   func(a *RoyalAct) { a.Reason = "" },
		"blank target":   func(a *RoyalAct) { a.Target = "  " },
		"blank effect":   func(a *RoyalAct) { a.Effect = "" },
		"missing date":   func(a *RoyalAct) { a.DecreedAt = time.Time{} },
		"missing vigour": func(a *RoyalAct) { a.Effective = time.Time{} },
	} {
		candidate := base
		mutate(&candidate)
		if _, err := DefineAct(candidate); !errors.Is(err, ErrIncompleteAct) && !errors.Is(err, ErrInvalidAuthority) {
			t.Fatalf("%s passed: %v", name, err)
		}
	}
	withoutID := base
	withoutID.ID = ""
	if _, err := DefineAct(withoutID); err == nil {
		t.Fatal("act without identity passed: incomplete decrees decide nothing")
	}
}

func TestDefineActRefusesHarmfulBackdating(t *testing.T) {
	base := wholeAct()
	base.Effective = base.DecreedAt.Add(-time.Nanosecond)
	if _, err := DefineAct(base); !errors.Is(err, ErrRetroactiveAct) {
		t.Fatalf("backdated = %v, want ErrRetroactiveAct", err)
	}
	atTick := wholeAct()
	atTick.Effective = atTick.DecreedAt
	if _, err := DefineAct(atTick); err != nil {
		t.Fatalf("effective at decree date: %v", err)
	}
}

func TestDefineActRefusesMissingOrigin(t *testing.T) {
	base := wholeAct()
	base.Kind = ActEconomic
	base.Origin = ""
	base.Amount = 1000
	if _, err := DefineAct(base); !errors.Is(err, ErrUnknownOrigin) {
		t.Fatalf("originless economic = %v, want ErrUnknownOrigin", err)
	}
	ghost := base
	ghost.Origin = "tesouro-livre"
	ghost.Amount = 0
	if _, err := DefineAct(ghost); err == nil {
		t.Fatal("zero-value economic passed: value never moves from nowhere")
	}
	live := base
	live.Origin = "tesouro-livre"
	live.Amount = 1000
	if _, err := DefineAct(live); err != nil {
		t.Fatalf("funded economic: %v", err)
	}
	plain := wholeAct()
	plain.Origin = "tesouro-livre"
	if _, err := DefineAct(plain); err == nil {
		t.Fatal("non-economic carrying origin passed: only the economic kind moves value")
	}
}

func TestDefineActRefusesAmbiguousCharter(t *testing.T) {
	base := wholeAct()
	for _, raw := range []string{"", "current", "latest", "v0", "V1", "v01", "1"} {
		candidate := base
		candidate.Charter = CharterVersion(raw)
		if _, err := DefineAct(candidate); !errors.Is(err, ErrAmbiguousCharter) {
			t.Fatalf("charter %q = %v, want ErrAmbiguousCharter", raw, err)
		}
	}
}

func TestDefineActLinksCorrectionWithoutRewriting(t *testing.T) {
	base := wholeAct()
	base.Corrects = "decreto-0"
	linked, err := DefineAct(base)
	if err != nil {
		t.Fatalf("linked correction: %v", err)
	}
	if linked.Corrects != "decreto-0" {
		t.Fatalf("link = %q, want the original act", linked.Corrects)
	}
	self := wholeAct()
	self.Corrects = self.ID
	if _, err := DefineAct(self); err == nil {
		t.Fatal("self-correction passed: a correction never rewrites itself")
	}
}
