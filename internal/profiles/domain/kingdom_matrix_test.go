package domain_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	disputes "github.com/AlexandreZanata/Regnovum/internal/disputes/domain"
	inquisition "github.com/AlexandreZanata/Regnovum/internal/inquisition/domain"
	institutions "github.com/AlexandreZanata/Regnovum/internal/institutions/domain"
	profiles "github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
	reputation "github.com/AlexandreZanata/Regnovum/internal/reputation/domain"
)

// matrixAnchor is the fixed instant of the access-matrix fixtures.
func matrixAnchor() time.Time {
	return time.Date(2026, time.March, 4, 10, 0, 0, 0, time.UTC)
}

// matrixActors is the closed actor vocabulary of the kingdom matrix.
func matrixActors() []string {
	return []string{"public", "party", "defender", "auditor", "king"}
}

// matrixFields is the closed field vocabulary of the kingdom matrix.
func matrixFields() []string {
	return []string{
		"livro-resumo",
		"livro-integra-autor",
		"livro-integra-mandato",
		"livro-integra-auditoria",
		"prova-digest",
		"prova-bruta",
		"contrato-termos",
		"contrato-aceite",
		"saldo-exato",
		"ranking-alias",
		"recibo",
		"export-contratos",
		"export-atos",
		"reputacao-pagamento",
	}
}

// kingdomMatrix is the certified access matrix: true allows, false denies.
// Public reads only summaries and aliases. The integra answers only to a
// listed auditor. Minimal proof answers only to the notified defense.
// Contracts, receipts and exports answer only to the titular (or the heir
// of a dead account). Exact balances and raw payloads answer to nobody in
// the kingdom surface, and payment never becomes reputation for anybody.
func kingdomMatrix() map[string]map[string]bool {
	allow := map[string]bool{
		"public": true, "party": true, "defender": true, "auditor": true, "king": true,
	}
	deny := map[string]bool{
		"public": false, "party": false, "defender": false, "auditor": false, "king": false,
	}
	auditorOnly := map[string]bool{
		"public": false, "party": false, "defender": false, "auditor": true, "king": false,
	}
	partyOnly := map[string]bool{
		"public": false, "party": true, "defender": false, "auditor": false, "king": false,
	}
	defenderOnly := map[string]bool{
		"public": false, "party": false, "defender": true, "auditor": false, "king": false,
	}
	return map[string]map[string]bool{
		"livro-resumo":            allow,
		"livro-integra-autor":     auditorOnly,
		"livro-integra-mandato":   auditorOnly,
		"livro-integra-auditoria": auditorOnly,
		"prova-digest":            defenderOnly,
		"prova-bruta":             deny,
		"contrato-termos":         partyOnly,
		"contrato-aceite":         partyOnly,
		"saldo-exato":             deny,
		"ranking-alias":           allow,
		"recibo":                  partyOnly,
		"export-contratos":        partyOnly,
		"export-atos":             partyOnly,
		"reputacao-pagamento":     deny,
	}
}

func matrixBook() institutions.Book {
	book, err := institutions.Append(institutions.Book{}, institutions.ActRequest{
		ID: "ato-matriz-1", Author: "autor-1", Office: "auditoria",
		Competence: "auditoria: conferir atos do reino",
		Rule:       "carta-v2: auditoria independente", Season: "temporada-1",
		Mandate: "designacao-1", Effect: "ato conferido e registrado",
		At: matrixAnchor(), Auditor: "auditor-2",
		AuditedAt: matrixAnchor().Add(time.Hour),
		Viewers:   []string{"auditor-1", "auditor-2"},
	})
	if err != nil {
		panic(err)
	}
	return book
}

func matrixProposal(t *testing.T) disputes.Proposal {
	t.Helper()
	proposal, err := disputes.Propose(disputes.ProposalRequest{
		Key: "negocio-matriz-1", Version: 1, Object: "lote de graos da temporada",
		Claimant: "ana", Respondent: "boa", ValueMilli: 1000,
		EscrowRef: "custodia-1", Rite: "rito-arbitral",
		Evidence: "regras-de-prova", Costs: disputes.CostsSplit,
		ExpiresAt: matrixAnchor().Add(48 * time.Hour), Execution: "entrega no celeiro",
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	return proposal
}

func matrixDigest() string {
	return strings.Repeat("ab", 32)
}

func matrixEnvelope(t *testing.T) inquisition.SealedEnvelope {
	t.Helper()
	envelope, err := inquisition.SealEvidence(inquisition.SealRequest{
		ID: "peca-matriz-1", Case: "caso-matriz-1",
		KindRaw: "documento", Digest: matrixDigest(),
		SealedAt: matrixAnchor(), Sealer: "inquisidor-1",
	})
	if err != nil {
		t.Fatalf("SealEvidence: %v", err)
	}
	return envelope
}

func matrixNotice(t *testing.T, envelope inquisition.SealedEnvelope) inquisition.DefenseNotice {
	t.Helper()
	notice, err := inquisition.NotifyDefense(envelope.ID, "defensor-1", matrixAnchor().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("NotifyDefense: %v", err)
	}
	return notice
}

func matrixExportRequest(account, requester string) profiles.ExportRequest {
	return profiles.ExportRequest{
		Account: account, Requester: requester,
		Sections: []string{"contratos", "recibos", "atos-do-titular"},
		Consent:  true, At: matrixAnchor(),
	}
}

func TestKingdomMatrixHasNoMissingCell(t *testing.T) {
	matrix := kingdomMatrix()
	for _, field := range matrixFields() {
		cells, known := matrix[field]
		if !known {
			t.Fatalf("matrix has no row for %q: every sensitive field needs five cells", field)
		}
		for _, actor := range matrixActors() {
			if _, known := cells[actor]; !known {
				t.Fatalf("matrix[%q] has no cell for %q: absence is failure, not public access", field, actor)
			}
		}
	}
	for field := range matrix {
		known := false
		for _, want := range matrixFields() {
			if field == want {
				known = true
			}
		}
		if !known {
			t.Fatalf("matrix carries unexpected %q: rows are closed", field)
		}
	}
	allowed := []string{"contratos", "recibos", "atos-do-titular"}
	for _, section := range allowed {
		if _, known := profiles.ParseExportSection(section); !known {
			t.Fatalf("export allowlist lost %q: removing a field must fail", section)
		}
	}
	denied := []string{"saldos", "prova-alheia", "balances", "recibo-extra", "contrato-extra", "", "CONTRATOS"}
	for _, section := range denied {
		if _, known := profiles.ParseExportSection(section); known {
			t.Fatalf("export allowlist accepts %q: exact balances and foreign proof never board an export", section)
		}
	}
}

func TestKingdomMatrixStructShapeHasNoUnlistedField(t *testing.T) {
	officeFields := map[string]bool{
		"ID": true, "Author": true, "Office": true, "Competence": true,
		"Rule": true, "Season": true, "Mandate": true, "Effect": true,
		"At": true, "Corrects": true, "Audit": true,
	}
	for i := 0; i < reflect.TypeOf(institutions.OfficeAct{}).NumField(); i++ {
		name := reflect.TypeOf(institutions.OfficeAct{}).Field(i).Name
		if !officeFields[name] {
			t.Fatalf("OfficeAct carries unlisted %q: map it into the matrix before it ships", name)
		}
	}
	summaryFields := map[string]bool{
		"ID": true, "Office": true, "Competence": true, "Rule": true,
		"Season": true, "Effect": true, "At": true, "Digest": true,
	}
	for i := 0; i < reflect.TypeOf(institutions.ActSummary{}).NumField(); i++ {
		name := reflect.TypeOf(institutions.ActSummary{}).Field(i).Name
		if !summaryFields[name] {
			t.Fatalf("ActSummary carries unlisted %q: public shape changes need a matrix row", name)
		}
	}
	standingFields := map[string]bool{
		"Subject": true, "Role": true, "Season": true,
		"MetDeadlines": true, "Reversals": true, "Conflicts": true,
		"Upheld": true, "EventIDs": true, "Corrections": true,
	}
	for i := 0; i < reflect.TypeOf(reputation.Standing{}).NumField(); i++ {
		name := reflect.TypeOf(reputation.Standing{}).Field(i).Name
		if !standingFields[name] {
			t.Fatalf("Standing carries unlisted %q: money must never enter by a new field", name)
		}
	}
	envelopeFields := map[string]bool{
		"ID": true, "Case": true, "Kind": true, "Digest": true,
		"SealedAt": true, "Sealer": true, "Victim": true, "Minor": true,
	}
	for i := 0; i < reflect.TypeOf(inquisition.SealedEnvelope{}).NumField(); i++ {
		name := reflect.TypeOf(inquisition.SealedEnvelope{}).Field(i).Name
		if !envelopeFields[name] {
			t.Fatalf("SealedEnvelope carries unlisted %q: proof shape changes need a matrix row", name)
		}
	}
	viewFields := map[string]bool{
		"Case": true, "Digest": true, "SealedAt": true,
		"RevealedAt": true, "Viewer": true, "Redacted": true,
	}
	for i := 0; i < reflect.TypeOf(inquisition.DisclosureView{}).NumField(); i++ {
		name := reflect.TypeOf(inquisition.DisclosureView{}).Field(i).Name
		if !viewFields[name] {
			t.Fatalf("DisclosureView carries unlisted %q: minimal access forbids new payload", name)
		}
	}
	for _, banned := range []string{"Payload", "Conteudo", "Content", "Raw", "Vitima", "Victim", "Menor", "Minor"} {
		for i := 0; i < reflect.TypeOf(inquisition.DisclosureView{}).NumField(); i++ {
			if reflect.TypeOf(inquisition.DisclosureView{}).Field(i).Name == banned {
				t.Fatalf("DisclosureView carries %q: minimal access only", banned)
			}
		}
	}
	proposalFields := map[string]bool{
		"Key": true, "Version": true, "Object": true, "Claimant": true,
		"Respondent": true, "ValueMilli": true, "EscrowRef": true, "Rite": true,
		"Evidence": true, "Costs": true, "ExpiresAt": true, "Execution": true,
		"Accepted": true, "Hash": true,
	}
	for i := 0; i < reflect.TypeOf(disputes.Proposal{}).NumField(); i++ {
		name := reflect.TypeOf(disputes.Proposal{}).Field(i).Name
		if !proposalFields[name] {
			t.Fatalf("Proposal carries unlisted %q: contract shape changes need a matrix row", name)
		}
	}
	rankingFields := map[string]bool{"Alias": true, "ExactBalance": true}
	for i := 0; i < reflect.TypeOf(profiles.RankingDisclosure{}).NumField(); i++ {
		name := reflect.TypeOf(profiles.RankingDisclosure{}).Field(i).Name
		if !rankingFields[name] {
			t.Fatalf("RankingDisclosure carries unlisted %q: alias and exact balance stay distinct", name)
		}
	}
}

func TestKingdomMatrixBooksSeparateSummaryFromIntegra(t *testing.T) {
	book := matrixBook()
	record := book.Records[0]
	summary := institutions.Summarize(record)
	if summary.Office == "" || summary.Digest == "" {
		t.Fatal("public summary is empty: livro-resumo must stay readable")
	}
	canonical := summary.ID + summary.Office + summary.Competence + summary.Rule + string(summary.Season) + summary.Effect
	for _, leak := range []string{"autor-1", "designacao-1", "auditor-2"} {
		if strings.Contains(canonical, leak) {
			t.Fatalf("summary leaks %q: author, mandate and audit stay in the integra", leak)
		}
	}
	if _, err := institutions.ReadFull(book, "ato-matriz-1", "auditor-2"); err != nil {
		t.Fatalf("listed auditor: %v", err)
	}
	for actor, reader := range map[string]string{
		"public": "", "party": "ana", "defender": "defensor-1", "king": "rei-1",
	} {
		if _, err := institutions.ReadFull(book, "ato-matriz-1", reader); !errors.Is(err, institutions.ErrAccessDenied) {
			t.Fatalf("%s full read = %v, want ErrAccessDenied", actor, err)
		}
	}
	if _, err := institutions.ReadFull(book, "ato-matriz-1", "estranho-1"); !errors.Is(err, institutions.ErrAccessDenied) {
		t.Fatalf("stranger full read = %v, want ErrAccessDenied", err)
	}
}

func TestKingdomMatrixProofAnswersOnlyDefenderMinimally(t *testing.T) {
	envelope := matrixEnvelope(t)
	notice := matrixNotice(t, envelope)
	view, err := inquisition.RevealForDefense(inquisition.RevealRequest{
		Envelope: envelope, Notice: notice, Viewer: "defensor-1",
		Digest: matrixDigest(), At: matrixAnchor().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("notified defense: %v", err)
	}
	if view.Digest != matrixDigest() || view.Case != envelope.Case {
		t.Fatalf("view = %+v, want the binding without raw payload", view)
	}
	for actor, viewer := range map[string]string{
		"public": "estranho-1", "party": "ana", "auditor": "auditor-1", "king": "rei-1",
	} {
		req := inquisition.RevealRequest{
			Envelope: envelope, Notice: notice, Viewer: inquisition.Subject(viewer),
			Digest: matrixDigest(), At: matrixAnchor().Add(time.Hour),
		}
		if _, err := inquisition.RevealForDefense(req); !errors.Is(err, inquisition.ErrUnnotifiedDefense) {
			t.Fatalf("%s proof view = %v, want ErrUnnotifiedDefense", actor, err)
		}
	}
	victim, err := inquisition.SealEvidence(inquisition.SealRequest{
		ID: "peca-matriz-vitima", Case: "caso-matriz-1",
		KindRaw: "documento", Digest: matrixDigest(),
		SealedAt: matrixAnchor(), Sealer: "inquisidor-1", Victim: true,
	})
	if err != nil {
		t.Fatalf("SealEvidence victim: %v", err)
	}
	victimNotice, err := inquisition.NotifyDefense(victim.ID, "defensor-1", matrixAnchor().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("NotifyDefense victim: %v", err)
	}
	bare := inquisition.RevealRequest{
		Envelope: victim, Notice: victimNotice, Viewer: "defensor-1",
		Digest: matrixDigest(), At: matrixAnchor().Add(time.Hour),
	}
	if _, err := inquisition.RevealForDefense(bare); !errors.Is(err, inquisition.ErrExposedVictim) {
		t.Fatalf("unredacted victim = %v, want ErrExposedVictim", err)
	}
	for _, raw := range []string{"executavel", "script"} {
		req := inquisition.SealRequest{
			ID: "peca-matriz-perigo", Case: "caso-matriz-1",
			KindRaw: raw, Digest: matrixDigest(),
			SealedAt: matrixAnchor(), Sealer: "inquisidor-1",
		}
		if _, err := inquisition.SealEvidence(req); !errors.Is(err, inquisition.ErrUnsafeEvidence) {
			t.Fatalf("dangerous %q = %v, want ErrUnsafeEvidence", raw, err)
		}
	}
}

func TestKingdomMatrixContractsBindOnlyParties(t *testing.T) {
	proposal := matrixProposal(t)
	now := matrixAnchor().Add(time.Hour)
	if _, err := proposal.Accept("ana", now); err != nil {
		t.Fatalf("claimant accept: %v", err)
	}
	if _, err := proposal.Accept("boa", now); err != nil {
		t.Fatalf("respondent accept: %v", err)
	}
	for actor, stranger := range map[string]string{
		"public": "estranho-1", "defender": "defensor-1", "auditor": "auditor-1", "king": "rei-1",
	} {
		if _, err := proposal.Accept(stranger, now); !errors.Is(err, disputes.ErrTermsNotParty) {
			t.Fatalf("%s accept = %v, want ErrTermsNotParty", actor, err)
		}
	}
}

func TestKingdomMatrixBalancesAndReceiptsStayEntitled(t *testing.T) {
	exact := int64(800000000000)
	disclosed := profiles.AnonymizeRanking(profiles.RankingDisclosure{Alias: "ana-publica", ExactBalance: &exact})
	if disclosed.Alias != "ana-publica" || disclosed.ExactBalance != nil {
		t.Fatalf("disclosed = %+v, want alias for everybody and exact balance for nobody here", disclosed)
	}
	grant, err := profiles.AuthorizeExport(matrixExportRequest("ana", "ana"))
	if err != nil {
		t.Fatalf("titular export: %v", err)
	}
	if len(grant.Sections) != 3 {
		t.Fatalf("grant = %+v, want the three allowlisted sections", grant)
	}
	if err := profiles.CheckExportItems(grant, []profiles.ExportItem{{ID: "recibo-1", Owner: "ana"}}); err != nil {
		t.Fatalf("own receipt: %v", err)
	}
	refused := matrixExportRequest("ana", "ana")
	refused.Consent = false
	if _, err := profiles.AuthorizeExport(refused); !errors.Is(err, profiles.ErrConsentRefused) {
		t.Fatalf("public without consent = %v, want ErrConsentRefused", err)
	}
	dead := matrixExportRequest("ana", "boa")
	dead.Dead = true
	dead.Heir = "carlos"
	if _, err := profiles.AuthorizeExport(dead); !errors.Is(err, profiles.ErrDeadAccountAccess) {
		t.Fatalf("stranger over dead = %v, want ErrDeadAccountAccess", err)
	}
	heir := matrixExportRequest("ana", "boa")
	heir.Dead = true
	heir.Heir = "boa"
	if _, err := profiles.AuthorizeExport(heir); err != nil {
		t.Fatalf("heir export: %v", err)
	}
	mixed := []profiles.ExportItem{{ID: "recibo-9", Owner: "boa"}}
	if err := profiles.CheckExportItems(grant, mixed); !errors.Is(err, profiles.ErrCrossAccountExport) {
		t.Fatalf("mixed holders = %v, want ErrCrossAccountExport", err)
	}
	foreign := []profiles.ExportItem{{ID: "peca-9", Owner: "ana", ForeignProof: true}}
	if err := profiles.CheckExportItems(grant, foreign); !errors.Is(err, profiles.ErrCrossAccountExport) {
		t.Fatalf("foreign proof = %v, want ErrCrossAccountExport", err)
	}
}

func TestKingdomMatrixPaymentNeverBecomesStanding(t *testing.T) {
	record := reputation.StandingRecord{Subject: "ana", Role: "auditor", Season: "temporada-1"}
	for _, kind := range []string{"pagamento", "compra", "dizimo", "nota-9"} {
		req := reputation.EventRequest{
			Record: record, ID: "fato-pago", Kind: kind,
			At: matrixAnchor(), Link: "caso-pago", Detail: "tentativa de comprar nota",
		}
		if _, err := reputation.RecordEvent(req); !errors.Is(err, reputation.ErrUnknownEvent) {
			t.Fatalf("kind %q = %v, want ErrUnknownEvent for every actor", kind, err)
		}
	}
	kept, err := reputation.RecordEvent(reputation.EventRequest{
		Record: record, ID: "fato-1", Kind: "prazo-cumprido",
		At: matrixAnchor(), Link: "caso-fato-1", Detail: "fato auditavel fato-1",
	})
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	standing, err := reputation.Aggregate(kept)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if standing.Total() != 1 || standing.Subject != "ana" {
		t.Fatalf("standing = %+v, want identified facts without score", standing)
	}
}
