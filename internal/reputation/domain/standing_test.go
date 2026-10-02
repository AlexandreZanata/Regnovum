package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func standingAnchor() time.Time {
	return time.Date(2026, time.March, 2, 10, 0, 0, 0, time.UTC)
}

func standingRecord() StandingRecord {
	return StandingRecord{
		Subject: "ana", Role: "auditor", Season: "temporada-1",
	}
}

func recordStandingEvent(record StandingRecord, id, kind string) StandingRecord {
	next, err := RecordEvent(EventRequest{
		Record: record, ID: id, Kind: kind,
		At: standingAnchor(), Link: "caso-" + id, Detail: "fato auditavel " + id,
	})
	if err != nil {
		panic(err)
	}
	return next
}

func TestRoleStandingAggregatesVerifiableFacts(t *testing.T) {
	record := standingRecord()
	record = recordStandingEvent(record, "fato-1", "prazo-cumprido")
	record = recordStandingEvent(record, "fato-2", "reversao")
	record = recordStandingEvent(record, "fato-3", "conflito-declarado")
	record = recordStandingEvent(record, "fato-4", "decisao-mantida")
	standing, err := Aggregate(record)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if standing.MetDeadlines != 1 || standing.Reversals != 1 || standing.Conflicts != 1 || standing.Upheld != 1 {
		t.Fatalf("standing = %+v, want one fact of each kind", standing)
	}
	if standing.Total() != 4 || len(standing.EventIDs) != 4 {
		t.Fatalf("standing = %+v, want the identified history, never a single score", standing)
	}
	if standing.Subject != "ana" || standing.Role != "auditor" || standing.Season != "temporada-1" {
		t.Fatalf("standing = %+v, want subject, role and season identified", standing)
	}
	other := StandingRecord{Subject: "ana", Role: "auditor", Season: "temporada-2"}
	other = recordStandingEvent(other, "fato-5", "prazo-cumprido")
	reseasoned, err := Aggregate(other)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if reseasoned.Total() != 1 || reseasoned.Season != "temporada-2" {
		t.Fatalf("reseasoned = %+v, want seasons kept apart", reseasoned)
	}
}

func TestPaymentNeverBecomesStanding(t *testing.T) {
	record := standingRecord()
	for _, kind := range []string{"pagamento", "compra", "dizimo", "nota-9"} {
		req := EventRequest{
			Record: record, ID: "fato-pago", Kind: kind,
			At: standingAnchor(), Link: "caso-pago", Detail: "tentativa de comprar nota",
		}
		if _, err := RecordEvent(req); !errors.Is(err, ErrUnknownEvent) {
			t.Fatalf("kind %q = %v, want ErrUnknownEvent", kind, err)
		}
	}
	if _, err := ParseEventKind("pagamento"); !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("payment kind = %v, want ErrUnknownEvent", err)
	}
	banned := []string{"Payment", "Pagamento", "Price", "Preco", "Score", "Nota", "Grade", "Balance", "Saldo", "Wealth", "Riqueza", "Office", "Cargo", "Patent", "Patente", "Amount", "Valor", "Mint", "Treasury", "Tesouro"}
	for _, value := range []any{StandingEvent{}, Standing{}, StandingRecord{}} {
		for i := 0; i < reflect.TypeOf(value).NumField(); i++ {
			field := reflect.TypeOf(value).Field(i).Name
			for _, deny := range banned {
				if strings.Contains(field, deny) {
					t.Fatalf("%T carries %s: money buys no standing", value, field)
				}
			}
		}
	}
	replay, err := RecordEvent(EventRequest{
		Record: recordStandingEvent(record, "fato-1", "prazo-cumprido"),
		ID:     "fato-1", Kind: "prazo-cumprido",
		At: standingAnchor(), Link: "caso-fato-1", Detail: "fato auditavel fato-1",
	})
	if err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if len(replay.Events) != 1 {
		t.Fatalf("replay = %+v, want a single fact", replay)
	}
	divergent := EventRequest{
		Record: replay, ID: "fato-1", Kind: "reversao",
		At: standingAnchor(), Link: "caso-fato-1", Detail: "fato auditavel fato-1",
	}
	if _, err := RecordEvent(divergent); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("divergent replay = %v, want ErrEventConflict", err)
	}
}

func TestCorrectionLinksToItsFact(t *testing.T) {
	record := recordStandingEvent(standingRecord(), "fato-1", "decisao-mantida")
	corrected, err := CorrectEvent(CorrectionRequest{
		Record: record, ID: "correcao-1", Corrects: "fato-1",
		At: standingAnchor().Add(time.Hour), Detail: "errata vinculada aos autos",
	})
	if err != nil {
		t.Fatalf("CorrectEvent: %v", err)
	}
	if len(corrected.Events) != 1 || len(corrected.Corrections) != 1 {
		t.Fatalf("corrected = %+v, want the fact kept beside its correction", corrected)
	}
	standing, err := Aggregate(corrected)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if standing.Upheld != 1 || standing.Corrections != 1 {
		t.Fatalf("standing = %+v, want counts unchanged by annotation", standing)
	}
	floating := CorrectionRequest{
		Record: record, ID: "correcao-2", Corrects: "fato-inexistente",
		At: standingAnchor().Add(time.Hour), Detail: "correcao sem fato",
	}
	if _, err := CorrectEvent(floating); !errors.Is(err, ErrUnknownFact) {
		t.Fatalf("floating correction = %v, want ErrUnknownFact", err)
	}
	again, err := CorrectEvent(CorrectionRequest{
		Record: corrected, ID: "correcao-1", Corrects: "fato-1",
		At: standingAnchor().Add(time.Hour), Detail: "errata vinculada aos autos",
	})
	if err != nil || len(again.Corrections) != 1 {
		t.Fatalf("correction replay = %v, want the same single link", err)
	}
}

func TestProfessionNeverInheritsAndDeletionFollowsPolicy(t *testing.T) {
	record := recordStandingEvent(standingRecord(), "fato-1", "prazo-cumprido")
	executor := StandingRecord{Subject: "ana", Role: "executor", Season: "temporada-1"}
	alien, err := Aggregate(executor)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if alien.Total() != 0 {
		t.Fatalf("alien = %+v, want no inherited note across professions", alien)
	}
	forgetHeld := ForgetRequest{
		Record: record, EventID: "fato-1",
		RetentionDue: true, LegalHold: true, At: standingAnchor().Add(2 * time.Hour),
	}
	if _, err := ForgetEvent(forgetHeld); !errors.Is(err, ErrForgetBlocked) {
		t.Fatalf("held deletion = %v, want ErrForgetBlocked", err)
	}
	forgetEarly := ForgetRequest{
		Record: record, EventID: "fato-1",
		RetentionDue: false, At: standingAnchor().Add(2 * time.Hour),
	}
	if _, err := ForgetEvent(forgetEarly); !errors.Is(err, ErrForgetBlocked) {
		t.Fatalf("early deletion = %v, want ErrForgetBlocked", err)
	}
	linked := recordStandingEvent(standingRecord(), "fato-2", "reversao")
	linked, err = CorrectEvent(CorrectionRequest{
		Record: linked, ID: "correcao-2", Corrects: "fato-2",
		At: standingAnchor().Add(time.Hour), Detail: "errata vinculada aos autos",
	})
	if err != nil {
		t.Fatalf("CorrectEvent: %v", err)
	}
	forgetLinked := ForgetRequest{
		Record: linked, EventID: "fato-2",
		RetentionDue: true, At: standingAnchor().Add(2 * time.Hour),
	}
	if _, err := ForgetEvent(forgetLinked); !errors.Is(err, ErrForgetBlocked) {
		t.Fatalf("linked deletion = %v, want ErrForgetBlocked", err)
	}
	released, err := ForgetEvent(ForgetRequest{
		Record: record, EventID: "fato-1",
		RetentionDue: true, At: standingAnchor().Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("ForgetEvent: %v", err)
	}
	standing, err := Aggregate(released)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if standing.Total() != 0 {
		t.Fatalf("standing = %+v, want the released fact forgotten", standing)
	}
	if _, err := ForgetEvent(ForgetRequest{
		Record: released, EventID: "fato-1",
		RetentionDue: true, At: standingAnchor().Add(3 * time.Hour),
	}); !errors.Is(err, ErrUnknownFact) {
		t.Fatalf("second deletion = %v, want ErrUnknownFact", err)
	}
}

func TestHistoricMeritGrantsNothingCurrent(t *testing.T) {
	record := standingRecord()
	record = recordStandingEvent(record, "fato-1", "decisao-mantida")
	record = recordStandingEvent(record, "fato-2", "decisao-mantida")
	standing, err := Aggregate(record)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if standing.Upheld != 2 || standing.Total() != 2 {
		t.Fatalf("standing = %+v, want facts without entitlements", standing)
	}
	banned := []string{"Wealth", "Riqueza", "Office", "Cargo", "Patent", "Patente", "Treasury", "Tesouro", "Mint", "Grant", "Concede"}
	for _, value := range []any{Standing{}, StandingRecord{}, StandingEvent{}, Correction{}} {
		for i := 0; i < reflect.TypeOf(value).NumField(); i++ {
			field := reflect.TypeOf(value).Field(i).Name
			for _, deny := range banned {
				if strings.Contains(field, deny) {
					t.Fatalf("%T carries %s: historic merit grants nothing current", value, field)
				}
			}
		}
	}
	shapeless := StandingRecord{Subject: "ana", Role: "auditor"}
	if _, err := Aggregate(shapeless); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("seasonless standing = %v, want ErrInvalidRecord", err)
	}
}
