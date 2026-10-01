package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func recordAnchor() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func recordAct() RoyalAct {
	anchor := recordAnchor()
	return RoyalAct{
		ID: "decreto-30", Author: "rainha-1", Season: "temporada-1",
		Reign: 2, Competence: "patrimonial", Kind: ActEconomic,
		Reason: "reparacao devida a ana sem expor a vitima",
		Target: "conta/ana", Effect: "transferir 1000 de tesouro-livre sem mint",
		DecreedAt: anchor, Effective: anchor.Add(time.Hour),
		EndsAt:  anchor.Add(30 * 24 * time.Hour),
		Charter: "v3", Origin: "tesouro-livre", Amount: 1000,
	}
}

func TestSummarizePublishesAttributableRecord(t *testing.T) {
	act := recordAct()
	record, err := Summarize(act, "recurso-4")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if record.Act != act.ID || record.Author != act.Author || record.Season != act.Season || record.Reign != act.Reign {
		t.Fatalf("record = %+v, want the same act/book/reign/authorship", record)
	}
	digest, err := ActDigest(act)
	if err != nil {
		t.Fatalf("ActDigest: %v", err)
	}
	if record.Digest != digest {
		t.Fatalf("digest = %q vs %q, want the auditable seal binding the payload", record.Digest, digest)
	}
	if record.Kind != ActEconomic || record.Appeal != "recurso-4" {
		t.Fatalf("record = %+v, want the effect category and the appeal reference", record)
	}
}

func TestPublicRecordCarriesNoPayload(t *testing.T) {
	allowed := map[string]bool{
		"Act": true, "Author": true, "Season": true, "Reign": true,
		"Kind": true, "Charter": true, "DecreedAt": true,
		"Effective": true, "EndsAt": true, "Digest": true,
		"Corrects": true, "Appeal": true,
	}
	for i := 0; i < reflect.TypeOf(PublicRecord{}).NumField(); i++ {
		name := reflect.TypeOf(PublicRecord{}).Field(i).Name
		if !allowed[name] {
			t.Fatalf("field %q is not public vocabulary: payload never reaches the public", name)
		}
	}
	for _, banned := range []string{"Reason", "Target", "Effect", "Beneficiary", "Vault", "Origin", "Amount"} {
		if _, ok := reflect.TypeOf(PublicRecord{}).FieldByName(banned); ok {
			t.Fatalf("field %q exposes payload: victims never appear in public", banned)
		}
	}
	record, err := Summarize(recordAct(), "")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	canonical := record.Canonical()
	for _, secret := range []string{"ana", "vitima", "transferir 1000", "tesouro-livre", "reparacao"} {
		if strings.Contains(canonical, secret) {
			t.Fatalf("canonical leaks %q: hash auditavel nao expoe prova", secret)
		}
	}
}

func TestBookNeverErasesAndLinksCorrections(t *testing.T) {
	var book RoyalBook
	record, err := Summarize(recordAct(), "")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if err := book.Append(record); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := book.Append(record); err != nil {
		t.Fatalf("replay Append: %v", err)
	}
	if got := len(book.Records()); got != 1 {
		t.Fatalf("records = %d, want one entry: replay never duplicates", got)
	}
	divergent := record
	divergent.Digest = strings.Repeat("0", 64)
	if err := book.Append(divergent); !errors.Is(err, ErrTamperedAct) {
		t.Fatalf("divergent = %v, want ErrTamperedAct", err)
	}
	correction := recordAct()
	correction.ID = "decreto-31"
	correction.Corrects = "decreto-30"
	linked, err := Summarize(correction, "")
	if err != nil {
		t.Fatalf("Summarize correction: %v", err)
	}
	orphan := linked
	orphan.Corrects = "decreto-99"
	var alone RoyalBook
	if err := alone.Append(orphan); !errors.Is(err, ErrDetachedCorrection) {
		t.Fatalf("detached = %v, want ErrDetachedCorrection", err)
	}
	if err := book.Append(linked); err != nil {
		t.Fatalf("Append correction: %v", err)
	}
	original, err := book.Find("decreto-30")
	if err != nil {
		t.Fatalf("Find original: %v", err)
	}
	if original.Act != "decreto-30" {
		t.Fatalf("original = %+v, want the original staying visible", original)
	}
}

func TestBookRefusesHiddenActsAndAnonymousLogs(t *testing.T) {
	var book RoyalBook
	if err := AuditCoverage([]ActID{"decreto-30"}, book); !errors.Is(err, ErrUnrecordedAct) {
		t.Fatalf("hidden = %v, want ErrUnrecordedAct", err)
	}
	record, err := Summarize(recordAct(), "")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if err := book.Append(record); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := AuditCoverage([]ActID{"decreto-30"}, book); err != nil {
		t.Fatalf("covered: %v", err)
	}
	anonymous := record
	anonymous.Author = ""
	if err := book.Append(anonymous); !errors.Is(err, ErrAnonymousRecord) {
		t.Fatalf("anonymous = %v, want ErrAnonymousRecord", err)
	}
}

func TestBookHasNoDeletePath(t *testing.T) {
	allowed := map[string]bool{"Append": true, "Find": true, "Records": true}
	pointer := reflect.TypeOf(&RoyalBook{})
	for i := 0; i < pointer.NumMethod(); i++ {
		if !allowed[pointer.Method(i).Name] {
			t.Fatalf("method %q escapes the append-only surface: facts are never rewritten or erased", pointer.Method(i).Name)
		}
	}
	if pointer.NumMethod() != len(allowed) {
		t.Fatalf("methods = %d, want %d: no update and no delete exist", pointer.NumMethod(), len(allowed))
	}
}
