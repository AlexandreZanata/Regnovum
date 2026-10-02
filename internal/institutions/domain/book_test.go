package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func bookAnchor() time.Time {
	return time.Date(2026, time.March, 3, 10, 0, 0, 0, time.UTC)
}

func bookRequest(id string) ActRequest {
	return ActRequest{
		ID: id, Author: "auditor-1", Office: "auditoria",
		Competence: "auditoria: conferir atos do reino",
		Rule:       "carta-v2: auditoria independente", Season: "temporada-1",
		Mandate: "designacao-1", Effect: "ato conferido e registrado",
		At: bookAnchor(), Auditor: "auditor-2",
		AuditedAt: bookAnchor().Add(time.Hour),
		Viewers:   []string{"auditor-1", "auditor-2"},
	}
}

func appendBook(book Book, id string) Book {
	next, err := Append(book, bookRequest(id))
	if err != nil {
		panic(err)
	}
	return next
}

func TestInstitutionalBookKeepsAuthorityAndAudit(t *testing.T) {
	var book Book
	book = appendBook(book, "ato-1")
	book = appendBook(book, "ato-2")
	if len(book.Records) != 2 {
		t.Fatalf("book = %+v, want two recorded acts", book)
	}
	recorded, err := ReadFull(book, "ato-1", "auditor-2")
	if err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if recorded.Act.Author != "auditor-1" || recorded.Act.Audit.Auditor != "auditor-2" {
		t.Fatalf("record = %+v, want author with independent audit", recorded)
	}
	summary := Summarize(recorded)
	if summary.Office != "auditoria" || summary.Season != "temporada-1" {
		t.Fatalf("summary = %+v, want function, rule and season", summary)
	}
	if summary.Digest == "" || !strings.Contains(summary.Digest, "ato-1") {
		t.Fatalf("summary = %+v, want a digest bound to the act", summary)
	}
	corrected, err := Append(book, ActRequest{
		ID: "ato-3", Author: "auditor-1", Office: "auditoria",
		Competence: "auditoria: conferir atos do reino",
		Rule:       "carta-v2: auditoria independente", Season: "temporada-1",
		Mandate: "designacao-1", Effect: "errata vinculada ao ato-1",
		At: bookAnchor().Add(2 * time.Hour), Corrects: "ato-1",
		Auditor: "auditor-2", AuditedAt: bookAnchor().Add(3 * time.Hour),
		Viewers: []string{"auditor-2"},
	})
	if err != nil {
		t.Fatalf("linked correction: %v", err)
	}
	if len(corrected.Records) != 3 {
		t.Fatalf("corrected = %+v, want the original kept beside its correction", corrected)
	}
}

func TestActWithoutAuthorityOrAuditFails(t *testing.T) {
	var book Book
	mandateless := bookRequest("ato-4")
	mandateless.Mandate = ""
	if _, err := Append(book, mandateless); !errors.Is(err, ErrInvalidAct) {
		t.Fatalf("mandateless act = %v, want ErrInvalidAct", err)
	}
	auditless := bookRequest("ato-5")
	auditless.Auditor = ""
	if _, err := Append(book, auditless); !errors.Is(err, ErrInvalidAct) {
		t.Fatalf("auditless act = %v, want ErrInvalidAct", err)
	}
	selfAudited := bookRequest("ato-6")
	selfAudited.Auditor = "auditor-1"
	if _, err := Append(book, selfAudited); !errors.Is(err, ErrUnauthorizedAct) {
		t.Fatalf("self audit = %v, want ErrUnauthorizedAct", err)
	}
	backdated := bookRequest("ato-7")
	backdated.AuditedAt = bookAnchor().Add(-time.Hour)
	if _, err := Append(book, backdated); !errors.Is(err, ErrInvalidAct) {
		t.Fatalf("backdated audit = %v, want ErrInvalidAct", err)
	}
	floating := bookRequest("ato-8")
	floating.Corrects = "ato-inexistente"
	if _, err := Append(book, floating); !errors.Is(err, ErrUnknownCorrection) {
		t.Fatalf("floating correction = %v, want ErrUnknownCorrection", err)
	}
	selfCorrecting := bookRequest("ato-9")
	selfCorrecting.Corrects = "ato-9"
	if _, err := Append(book, selfCorrecting); !errors.Is(err, ErrUnknownCorrection) {
		t.Fatalf("self correction = %v, want ErrUnknownCorrection", err)
	}
}

func TestPublicSummaryCarriesNoPII(t *testing.T) {
	book := appendBook(Book{}, "ato-10")
	summary := Summarize(book.Records[0])
	banned := []string{"Subject", "Account", "Author", "Name", "Email", "Holder", "Victim", "Minor", "Address", "Phone", "Document", "Titular"}
	for i := 0; i < reflect.TypeOf(summary).NumField(); i++ {
		name := reflect.TypeOf(summary).Field(i).Name
		for _, deny := range banned {
			if strings.Contains(name, deny) {
				t.Fatalf("ActSummary carries %s: the public summary names the function, never the person", name)
			}
		}
	}
	canonical := summary.ID + summary.Office + summary.Competence + summary.Rule + string(summary.Season) + summary.Effect
	for _, leak := range []string{"auditor-1", "auditor-2", "ana"} {
		if strings.Contains(canonical, leak) {
			t.Fatalf("summary leaks %q: personal identifiers stay in the integra", leak)
		}
	}
}

func TestFullRecordStaysUnderAccessControl(t *testing.T) {
	book := appendBook(Book{}, "ato-11")
	if _, err := ReadFull(book, "ato-11", "auditor-2"); err != nil {
		t.Fatalf("listed auditor: %v", err)
	}
	if _, err := ReadFull(book, "ato-11", "estranho-1"); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("stranger full read = %v, want ErrAccessDenied", err)
	}
	if _, err := ReadFull(book, "ato-11", ""); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("public full read = %v, want ErrAccessDenied", err)
	}
	summary := Summarize(book.Records[0])
	if summary.ID != "ato-11" {
		t.Fatalf("summary = %+v, want the public reading the safe summary", summary)
	}
	if _, err := ReadFull(book, "ato-inexistente", "auditor-2"); !errors.Is(err, ErrUnknownCorrection) {
		t.Fatalf("missing act = %v, want ErrUnknownCorrection", err)
	}
}

func TestReplayKeepsASingleBook(t *testing.T) {
	book := appendBook(Book{}, "ato-12")
	again, err := Append(book, bookRequest("ato-12"))
	if err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if len(again.Records) != 1 {
		t.Fatalf("replay = %+v, want a single entry", again)
	}
	divergent := bookRequest("ato-12")
	divergent.Effect = "efeito trocado depois do registro"
	if _, err := Append(book, divergent); !errors.Is(err, ErrEntryConflict) {
		t.Fatalf("divergent replay = %v, want ErrEntryConflict", err)
	}
	shapeless := bookRequest("")
	if _, err := Append(book, shapeless); !errors.Is(err, ErrInvalidAct) {
		t.Fatalf("nameless act = %v, want ErrInvalidAct", err)
	}
}
