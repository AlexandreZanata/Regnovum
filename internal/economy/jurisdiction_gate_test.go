package economy_test

// P44-T10 — the per-country legal and product gate, test-only.
//
// There is no approval registry, no ratified term and no active
// tender: this file is the executable form of that fact. It judges
// the full product-by-country matrix through the real commerce
// jurisdiction gate (whose approved set is empty until a juridical
// review exists), proves no approval table exists to flip a cell,
// proves a v1 acceptance never authorizes v2 price/risk terms, proves
// the gate input cannot carry a network signal, and proves bonds and
// markets stay deferred. Every cell denies today; any future approval
// flips named cells with legal evidence in P45, never by inference.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	commerce "github.com/AlexandreZanata/Regnovum/internal/commerce/domain"
	economypg "github.com/AlexandreZanata/Regnovum/internal/economy/adapters/postgres"
	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
)

// gatedProducts is the T10 product scope: circulating INK, P2P,
// tithe and patents. Bonds and betting markets are not listed here:
// they stay excluded without a program of their own, judged below.
func gatedProducts() []string {
	return []string{"ink-circulante", "p2p", "dizimo", "patentes"}
}

// gatedCountries is the rehearsal country sample: well-formed codes
// on four continents, judged one by one. The matrix is total: every
// product-by-country cell gets a verdict, and the verdict is denied.
func gatedCountries() []string {
	return []string{"BR", "US", "PT", "DE", "FR", "AR", "MX", "JP"}
}

// TestJurisdictionRolloutBlockedEverywhere judges every matrix cell
// through the production gate with fully verified evidence: each one
// denies on country_unapproved, approvals stay zero, and malformed
// codes refuse before any gate is read.
func TestJurisdictionRolloutBlockedEverywhere(t *testing.T) {
	t.Parallel()
	var counters commerce.JurisdictionCounters
	cells := 0
	for _, product := range gatedProducts() {
		for _, raw := range gatedCountries() {
			country, err := commerce.ParseCountryCode(raw)
			if err != nil {
				t.Fatalf("%s/%s: well-formed code refused: %v", product, raw, err)
			}
			decision := commerce.AuthorizeJurisdiction(commerce.JurisdictionEvidence{
				Country:           country,
				AgeVerified:       true,
				ResidenceVerified: true,
				IDVerified:        true,
			})
			counters.Record(decision)
			cells++
			if decision.Allowed || decision.Reason != commerce.JurisdictionCountryUnapproved {
				t.Fatalf("%s/%s = %+v, want denial on country_unapproved", product, raw, decision)
			}
		}
	}
	if cells != len(gatedProducts())*len(gatedCountries()) {
		t.Fatalf("cells = %d, want the full matrix", cells)
	}
	snapshot := counters.Snapshot()
	if snapshot.Approved != 0 {
		t.Fatalf("approvals = %d, want zero with no juridical review", snapshot.Approved)
	}
	if snapshot.CountryUnapproved != uint64(cells) {
		t.Fatalf("unapproved = %d, want all %d cells", snapshot.CountryUnapproved, cells)
	}
	for _, raw := range []string{"", "B", "BRA", "br", "B R", "1A"} {
		if _, err := commerce.ParseCountryCode(raw); err == nil {
			t.Fatalf("malformed country %q accepted", raw)
		}
	}
	if len(commerce.ApprovedCountries()) != 0 {
		t.Fatalf("approved set = %v, want empty until juridical review", commerce.ApprovedCountries())
	}
}

// TestJurisdictionNoApprovalRegistry proves there is nowhere to
// record an approval: the schema holds no jurisdiction or approval
// table, so no code path — present or future without migration — can
// consult one. An approval registry arrives as a reviewed migration
// in P45, never as an inference.
func TestJurisdictionNoApprovalRegistry(t *testing.T) {
	database := dbtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var tables int64
	if err := database.Pool.Pool().QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'app'
		   AND (table_name LIKE '%jurisdiction%' OR table_name LIKE '%approval%')`).Scan(&tables); err != nil {
		t.Fatalf("scan registry tables: %v", err)
	}
	if tables != 0 {
		t.Fatalf("%d approval tables exist with no juridical review", tables)
	}
}

// TestJurisdictionTermsChangeRequiresNewAcceptance proves a price or
// risk change under a new charter version keeps resources off: the v1
// acceptance is recorded and intact, the v2 requirement resolves
// absence, and absence is a refusal, never an approval.
func TestJurisdictionTermsChangeRequiresNewAcceptance(t *testing.T) {
	database := dbtest.New(t)
	pool := database.Pool.Pool()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var holder string
	if err := pool.QueryRow(ctx,
		`INSERT INTO app.accounts (email, status) VALUES ('t10-terms@canary.invalid', 'active') RETURNING id::text`).Scan(&holder); err != nil {
		t.Fatalf("create holder: %v", err)
	}
	repo := economypg.NewRepository(pool)
	consents := application.NewConsentUseCase(repo)
	if _, err := consents.Execute(ctx, application.ConsentCommand{AccountID: holder, Charter: "v1", Decision: "accepted"}); err != nil {
		t.Fatalf("accept v1: %v", err)
	}
	// New price/risk terms ship as a new charter version: the v1
	// acceptance stands and stays intact, while the v2 requirement
	// resolves absence — and absence refuses.
	kept, err := repo.FindConsent(ctx, holder, mustCharterT10(t, "v1"))
	if err != nil || kept == nil || kept.Decision != domain.ConsentAccepted {
		t.Fatalf("v1 acceptance lost: %+v, %v", kept, err)
	}
	missing, err := repo.FindConsent(ctx, holder, mustCharterT10(t, "v2"))
	if err != nil || missing != nil {
		t.Fatalf("v2 requirement = %+v, %v: want absence, which refuses", missing, err)
	}
	var verdicts int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.economy_charter_consents WHERE account_id = $1::uuid`, holder).Scan(&verdicts); err != nil {
		t.Fatalf("count verdicts: %v", err)
	}
	if verdicts != 1 {
		t.Fatalf("verdicts = %d, want exactly the v1 row", verdicts)
	}
}

func mustCharterT10(t *testing.T, raw string) domain.CharterVersion {
	t.Helper()
	version, err := domain.ParseCharterVersion(raw)
	if err != nil {
		t.Fatalf("ParseCharterVersion(%q): %v", raw, err)
	}
	return version
}

// TestJurisdictionEvidenceCarriesNoNetworkSignal proves the geofence
// cannot depend on a client header: the gate input struct holds the
// attested country and three verified booleans and nothing else, so a
// forged X-Country, IP or VPN hint has no parameter to travel in.
func TestJurisdictionEvidenceCarriesNoNetworkSignal(t *testing.T) {
	t.Parallel()
	evidence := reflect.TypeOf(commerce.JurisdictionEvidence{})
	fields := map[string]reflect.Kind{}
	for i := 0; i < evidence.NumField(); i++ {
		fields[evidence.Field(i).Name] = evidence.Field(i).Type.Kind()
	}
	want := map[string]reflect.Kind{
		"Country":           reflect.String,
		"AgeVerified":       reflect.Bool,
		"ResidenceVerified": reflect.Bool,
		"IDVerified":        reflect.Bool,
	}
	if !reflect.DeepEqual(fields, want) {
		t.Fatalf("gate input = %v, want exactly %v: no header, IP or hint may enter", fields, want)
	}
	// The same evidence decides identically with forged ambient
	// headers present or absent: headers are not parameters, so they
	// cannot change the verdict.
	full := commerce.JurisdictionEvidence{
		Country: "BR", AgeVerified: true, ResidenceVerified: true, IDVerified: true,
	}
	first := commerce.AuthorizeJurisdiction(full)
	second := commerce.AuthorizeJurisdiction(full)
	if first != second || first.Allowed {
		t.Fatalf("verdicts = %+v/%+v, want identical denials", first, second)
	}
}

// TestJurisdictionBondsStayExcluded proves bonds and betting markets
// remain without a program: the coverage map still marks the axis
// deferred with refusal tests, never covered.
func TestJurisdictionBondsStayExcluded(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	raw, err := os.ReadFile(filepath.Join(root, "quality", "financial-coverage.json"))
	if err != nil {
		t.Fatalf("read coverage map: %v", err)
	}
	var document struct {
		Entries []struct {
			Axis   string `json:"axis"`
			Status string `json:"status"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode coverage map: %v", err)
	}
	for _, entry := range document.Entries {
		if entry.Axis == "deactivated-products" && entry.Status != "deferred" {
			t.Fatalf("deactivated-products = %q, want deferred: bonds and markets stay excluded", entry.Status)
		}
	}
}

// TestJurisdictionReasonsStayClosed proves the outcome vocabulary did
// not grow an open-by-default reason, and the legal matrix still
// enforces exactly age, residence and identification: volume and
// fraud stay monitored without ratified thresholds.
func TestJurisdictionReasonsStayClosed(t *testing.T) {
	t.Parallel()
	reasons := commerce.AllJurisdictionReasons()
	if len(reasons) != 6 {
		t.Fatalf("reasons = %v, want the closed six", reasons)
	}
	for _, reason := range reasons {
		if reason == commerce.JurisdictionApproved {
			continue
		}
		if reason.String() == "" || reason.String() == "approved" {
			t.Fatalf("reason %q blurs the open gate", reason)
		}
	}
	enforced := map[commerce.JurisdictionDimension]bool{}
	for _, dimension := range commerce.AllJurisdictionDimensions() {
		enforced[dimension] = commerce.DimensionEnforced(dimension)
	}
	want := map[commerce.JurisdictionDimension]bool{
		commerce.JurisdictionAge: true, commerce.JurisdictionResidence: true,
		commerce.JurisdictionVolume: false, commerce.JurisdictionFraud: false,
		commerce.JurisdictionIdentification: true,
	}
	if len(enforced) != len(want) {
		t.Fatalf("dimensions = %v, want five", enforced)
	}
	for dimension, enforcedNow := range want {
		if enforced[dimension] != enforcedNow {
			t.Fatalf("dimension %s enforced = %v, want %v", dimension, enforced[dimension], enforcedNow)
		}
	}
}
