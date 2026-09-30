package domain

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParseCountryCodeKeepsISOShape(t *testing.T) {
	for _, raw := range []string{"BR", "US", "XX", "ZZ"} {
		code, err := ParseCountryCode(raw)
		if err != nil {
			t.Fatalf("ParseCountryCode(%q): %v", raw, err)
		}
		if code.String() != raw {
			t.Fatalf("round-trip %q failed", raw)
		}
	}
	for _, raw := range []string{"", " ", "B", "br", "Br", "BR ", " BR", "B1", "1B", "BRA", "B R", "B-R"} {
		if _, err := ParseCountryCode(raw); !errors.Is(err, ErrInvalidJurisdiction) {
			t.Fatalf("ParseCountryCode(%q) = nil, want ErrInvalidJurisdiction", raw)
		}
	}
}

func TestAuthorizeJurisdictionDeniesUnknownCountryAndUnverifiedAge(t *testing.T) {
	if got := AuthorizeJurisdiction(JurisdictionEvidence{}); got.Allowed || got.Reason != JurisdictionUnknownCountry {
		t.Fatalf("empty evidence = %+v, want denied unknown_country", got)
	}
	mustCountry := func(t *testing.T, raw string) CountryCode {
		t.Helper()
		code, err := ParseCountryCode(raw)
		if err != nil {
			t.Fatalf("ParseCountryCode(%q): %v", raw, err)
		}
		return code
	}
	country := mustCountry(t, "BR")
	if got := AuthorizeJurisdiction(JurisdictionEvidence{Country: country}); got.Allowed || got.Reason != JurisdictionAgeUnverified {
		t.Fatalf("unverified age = %+v, want denied age_unverified", got)
	}
	if got := AuthorizeJurisdiction(JurisdictionEvidence{Country: country, AgeVerified: true}); got.Allowed || got.Reason != JurisdictionResidenceUnverified {
		t.Fatalf("unverified residence = %+v, want denied residence_unverified", got)
	}
	if got := AuthorizeJurisdiction(JurisdictionEvidence{
		Country: country, AgeVerified: true, ResidenceVerified: true,
	}); got.Allowed || got.Reason != JurisdictionIdentificationUnverified {
		t.Fatalf("unverified identity = %+v, want denied identification_unverified", got)
	}
}

func TestAuthorizeJurisdictionDeniesEveryCountryWithoutLegalApproval(t *testing.T) {
	if approved := ApprovedCountries(); len(approved) != 0 {
		t.Fatalf("approved countries = %v, want none: approval is a juridical act outside this module", approved)
	}
	best := JurisdictionEvidence{
		Country: "BR", AgeVerified: true, ResidenceVerified: true, IDVerified: true,
	}
	if got := AuthorizeJurisdiction(best); got.Allowed || got.Reason != JurisdictionCountryUnapproved {
		t.Fatalf("best-formed evidence = %+v, want denied country_unapproved: no input bypasses the empty approval set, not even an internal call", got)
	}
	for _, raw := range []string{"US", "XX", "ZZ"} {
		code, err := ParseCountryCode(raw)
		if err != nil {
			t.Fatalf("ParseCountryCode(%q): %v", raw, err)
		}
		got := AuthorizeJurisdiction(JurisdictionEvidence{
			Country: code, AgeVerified: true, ResidenceVerified: true, IDVerified: true,
		})
		if got.Allowed {
			t.Fatalf("country %q approved without juridical review", raw)
		}
	}
}

func TestJurisdictionMatrixDeclaresFiveAxesWithoutRatifiedThresholds(t *testing.T) {
	dimensions := AllJurisdictionDimensions()
	if len(dimensions) != 5 {
		t.Fatalf("matrix = %d axes, want age, residence, volume, fraud and identification", len(dimensions))
	}
	for _, dimension := range []JurisdictionDimension{JurisdictionAge, JurisdictionResidence, JurisdictionIdentification} {
		if !DimensionEnforced(dimension) {
			t.Fatalf("dimension %q must enforce: it is structural, not a ratified number", dimension)
		}
	}
	for _, dimension := range []JurisdictionDimension{JurisdictionVolume, JurisdictionFraud} {
		if DimensionEnforced(dimension) {
			t.Fatalf("dimension %q must stay monitored: no threshold was ratified, so none may enforce by inference", dimension)
		}
	}
	if len(AllJurisdictionReasons()) != 6 {
		t.Fatalf("reasons = %d, want the closed six", len(AllJurisdictionReasons()))
	}
}

func TestJurisdictionEvidenceCarriesNoPII(t *testing.T) {
	evidence := reflect.TypeOf(JurisdictionEvidence{})
	if evidence.NumField() != 4 {
		t.Fatalf("evidence has %d fields, want exactly country plus three verified booleans: collection stays proportional", evidence.NumField())
	}
	strings := 0
	for i := 0; i < evidence.NumField(); i++ {
		field := evidence.Field(i)
		switch field.Type.Kind() {
		case reflect.String:
			strings++
			if field.Name != "Country" {
				t.Fatalf("evidence string field = %q, want only the attested country code", field.Name)
			}
		case reflect.Bool:
		default:
			t.Fatalf("evidence field %q is %s: only the country code and booleans travel", field.Name, field.Type.Kind())
		}
	}
	if strings != 1 {
		t.Fatalf("evidence carries %d strings, want exactly the country code", strings)
	}
}

func TestJurisdictionCountersHoldIntegersOnly(t *testing.T) {
	var counters JurisdictionCounters
	counters.Record(AuthorizeJurisdiction(JurisdictionEvidence{}))
	counters.Record(AuthorizeJurisdiction(JurisdictionEvidence{Country: "BR"}))
	counters.Record(AuthorizeJurisdiction(JurisdictionEvidence{Country: "BR", AgeVerified: true}))
	counters.Record(AuthorizeJurisdiction(JurisdictionEvidence{Country: "BR", AgeVerified: true, ResidenceVerified: true}))
	counters.Record(AuthorizeJurisdiction(JurisdictionEvidence{Country: "BR", AgeVerified: true, ResidenceVerified: true, IDVerified: true}))
	snapshot := counters.Snapshot()
	if snapshot.UnknownCountry != 1 || snapshot.AgeUnverified != 1 || snapshot.ResidenceUnverified != 1 ||
		snapshot.IdentificationUnverified != 1 || snapshot.CountryUnapproved != 1 || snapshot.Approved != 0 {
		t.Fatalf("counters = %+v, want one denial per reason and zero approvals", snapshot)
	}
	countersType := reflect.TypeOf(JurisdictionCounters{})
	for i := 0; i < countersType.NumField(); i++ {
		field := countersType.Field(i)
		if field.Type.Kind() != reflect.Uint64 {
			t.Fatalf("counter %q is %s: public metrics hold integers only, never accounts, countries or headers", field.Name, field.Type.Kind())
		}
	}
}

// TestStagedHTTPReadsNoNetworkSignal proves the staged commerce
// surface cannot be bypassed by VPN or header: the delivered handlers
// never read forwarding, geo or VPN hints, and the locale negotiates
// titles only, never decisions.
func TestStagedHTTPReadsNoNetworkSignal(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not answer")
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(file), "..", "adapters", "http"))
	if err != nil {
		t.Fatalf("read staged http dir: %v", err)
	}
	forbidden := []string{
		"X-Forwarded-For", "X-Real-IP", "CF-IPCountry", "CloudFront-Viewer-Country",
		"X-Country", "GeoIP", "geoip", "X-Vpn", "X-VPN",
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), "_test.go") || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "adapters", "http", entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, token := range forbidden {
			if strings.Contains(string(raw), token) {
				t.Fatalf("%s reads %q: network signals never decide jurisdiction", entry.Name(), token)
			}
		}
	}
}
