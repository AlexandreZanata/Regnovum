package application_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/economy/application"
	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

// stubTotalsRepository stands in for the PostgreSQL adapter where no
// database behavior is under test: it counts derivations so the cache
// behavior is observable, and replays one canned reading.
type stubTotalsRepository struct {
	calls   int
	reading *application.CustodyTotalsReading
	err     error
}

func (s *stubTotalsRepository) ReadCustodyTotals(_ context.Context, _ domain.SeasonKey) (*application.CustodyTotalsReading, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.reading, nil
}

// totalsClock is a movable clock: the cache window is observable
// without waiting for it.
type totalsClock struct {
	now time.Time
}

func (c *totalsClock) Now() time.Time { return c.now }

func totalsInstant() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }

func richTotalsReading() *application.CustodyTotalsReading {
	return &application.CustodyTotalsReading{
		SupplyMillis:   domain.GenesisSupplyMillis,
		TreasuryMillis: domain.GenesisSupplyMillis - 3600,
		Vaults: []application.VaultTotal{
			{Vault: domain.TreasuryVaultGenesisHome, Millis: domain.GenesisSupplyMillis - 11000},
			{Vault: domain.TreasuryVaultSovereignReserve, Millis: 1000},
			{Vault: domain.TreasuryVaultCommercialStock, Millis: 2000},
			{Vault: domain.TreasuryVaultOperatingCash, Millis: 4400},
			{Vault: domain.TreasuryVaultFree, Millis: 0},
		},
		ReserveMillis:        1000,
		CirculationMillis:    600,
		CirculationCustodies: 6,
		LockedMillis:         3000,
		LockedHolds:          5,
		Frozen:               false,
	}
}

func mustTotalsUseCase(stub *stubTotalsRepository, clock *totalsClock) *application.CustodyTotalsUseCase {
	uc, err := application.NewCustodyTotalsUseCase(stub, clock)
	if err != nil {
		panic(err)
	}
	return uc
}

func TestCustodyTotalsMatchAuditedReading(t *testing.T) {
	t.Parallel()

	stub := &stubTotalsRepository{reading: richTotalsReading()}
	clock := &totalsClock{now: totalsInstant()}
	snapshot, err := mustTotalsUseCase(stub, clock).Totals(context.Background(), "pt", domain.CompatSeasonKey)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if snapshot.SupplyMillis != domain.GenesisSupplyMillis {
		t.Fatalf("supply = %d, want S", snapshot.SupplyMillis)
	}
	if snapshot.TreasuryMillis != domain.GenesisSupplyMillis-3600 {
		t.Fatalf("treasury = %d, want the audited total", snapshot.TreasuryMillis)
	}
	if snapshot.ReserveMillis != 1000 || snapshot.CirculationMillis != 600 || snapshot.LockedMillis != 3000 {
		t.Fatalf("classes changed: %+v", snapshot)
	}
	if snapshot.CirculationSuppressed || snapshot.LockedSuppressed {
		t.Fatalf("rich cells suppressed: %+v", snapshot)
	}
	if snapshot.LockedHolds != 5 || snapshot.Frozen {
		t.Fatalf("holds/frozen changed: %+v", snapshot)
	}
	var vaultSum int64
	for _, position := range snapshot.Vaults {
		vaultSum += position.Millis
	}
	if len(snapshot.Vaults) != 5 || vaultSum != snapshot.TreasuryMillis {
		t.Fatalf("vaults do not add up to the treasury: %+v", snapshot.Vaults)
	}
	if snapshot.Locale != "pt" || !snapshot.GeneratedAt.Equal(totalsInstant()) {
		t.Fatalf("locale/instant not carried: %+v", snapshot)
	}
	if snapshot.CacheSeconds != domain.TotalsCacheSeconds {
		t.Fatalf("cache window = %d, want %d", snapshot.CacheSeconds, domain.TotalsCacheSeconds)
	}
	if snapshot.Labels.Title == "" || snapshot.Labels.Supply == "" {
		t.Fatalf("titles missing: %+v", snapshot.Labels)
	}
}

func TestCustodyTotalsSuppressSmallCells(t *testing.T) {
	t.Parallel()

	stub := &stubTotalsRepository{reading: &application.CustodyTotalsReading{
		SupplyMillis:         domain.GenesisSupplyMillis,
		TreasuryMillis:       domain.GenesisSupplyMillis - 300,
		Vaults:               []application.VaultTotal{{Vault: domain.TreasuryVaultGenesisHome, Millis: domain.GenesisSupplyMillis - 300}},
		ReserveMillis:        0,
		CirculationMillis:    200,
		CirculationCustodies: 2,
		LockedMillis:         600,
		LockedHolds:          1,
	}}
	snapshot, err := mustTotalsUseCase(stub, &totalsClock{now: totalsInstant()}).Totals(context.Background(), "en", domain.CompatSeasonKey)
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	if snapshot.CirculationMillis != 0 || !snapshot.CirculationSuppressed {
		t.Fatalf("small circulation not suppressed: %+v", snapshot)
	}
	if snapshot.LockedMillis != 0 || !snapshot.LockedSuppressed {
		t.Fatalf("small lock cell not suppressed: %+v", snapshot)
	}
	if snapshot.LockedHolds != 1 {
		t.Fatalf("hold count changed: %+v", snapshot)
	}
	if snapshot.SupplyMillis != domain.GenesisSupplyMillis || snapshot.TreasuryMillis != domain.GenesisSupplyMillis-300 {
		t.Fatalf("sovereign classes must never suppress: %+v", snapshot)
	}
}

func TestCustodyTotalsRefuseUnknownLocale(t *testing.T) {
	t.Parallel()

	stub := &stubTotalsRepository{reading: richTotalsReading()}
	if _, err := mustTotalsUseCase(stub, &totalsClock{now: totalsInstant()}).Totals(context.Background(), "fr", domain.CompatSeasonKey); !errors.Is(err, domain.ErrInvalidTotals) {
		t.Fatalf("Totals(fr) = %v, want ErrInvalidTotals", err)
	}
	if stub.calls != 0 {
		t.Fatalf("unknown locale reached the repository")
	}
}

func TestCustodyTotalsRefuseIncompleteComposition(t *testing.T) {
	t.Parallel()

	stub := &stubTotalsRepository{reading: richTotalsReading()}
	if _, err := application.NewCustodyTotalsUseCase(nil, &totalsClock{now: totalsInstant()}); !errors.Is(err, domain.ErrInvalidTotals) {
		t.Fatalf("nil repository = %v, want ErrInvalidTotals", err)
	}
	if _, err := application.NewCustodyTotalsUseCase(stub, nil); !errors.Is(err, domain.ErrInvalidTotals) {
		t.Fatalf("nil clock = %v, want ErrInvalidTotals", err)
	}
}

func TestCustodyTotalsCacheSharesOneDerivation(t *testing.T) {
	t.Parallel()

	stub := &stubTotalsRepository{reading: richTotalsReading()}
	clock := &totalsClock{now: totalsInstant()}
	uc := mustTotalsUseCase(stub, clock)

	first, err := uc.Totals(context.Background(), "pt", domain.CompatSeasonKey)
	if err != nil {
		t.Fatalf("first Totals: %v", err)
	}
	second, err := uc.Totals(context.Background(), "pt", domain.CompatSeasonKey)
	if err != nil {
		t.Fatalf("second Totals: %v", err)
	}
	if stub.calls != 1 {
		t.Fatalf("derivations = %d, want 1 shared inside the window", stub.calls)
	}
	if !second.GeneratedAt.Equal(first.GeneratedAt) {
		t.Fatalf("cached instant moved: %+v vs %+v", second.GeneratedAt, first.GeneratedAt)
	}
	clock.now = totalsInstant().Add((domain.TotalsCacheSeconds + 1) * time.Second)
	third, err := uc.Totals(context.Background(), "pt", domain.CompatSeasonKey)
	if err != nil {
		t.Fatalf("expired Totals: %v", err)
	}
	if stub.calls != 2 {
		t.Fatalf("derivations = %d, want a fresh one past the window", stub.calls)
	}
	if !third.GeneratedAt.Equal(clock.now) {
		t.Fatalf("re-derivation did not carry the clock: %+v", third.GeneratedAt)
	}
	if _, err := uc.Totals(context.Background(), "en", domain.CompatSeasonKey); err != nil {
		t.Fatalf("other locale Totals: %v", err)
	}
	if stub.calls != 3 {
		t.Fatalf("derivations = %d, want one per locale", stub.calls)
	}
}

func TestCustodyTotalsSnapshotCarriesNoPerson(t *testing.T) {
	t.Parallel()

	// The document must rebuild from closed vocabularies only:
	// reflection over every field refuses maps, identifiers and
	// free-form strings, and every string value must belong to the
	// locale dictionary, the locale codes or the vault vocabulary.
	dictionary := map[string]bool{"pt": true, "en": true}
	for _, locale := range domain.AllTotalsLocales() {
		for _, labels := range []domain.TotalsLabels{domain.TotalsLabelsFor(locale)} {
			values := reflect.ValueOf(labels)
			for i := 0; i < values.NumField(); i++ {
				dictionary[values.Field(i).String()] = true
			}
		}
	}
	vaults := map[string]bool{}
	for _, vault := range domain.AllTreasuryVaults() {
		vaults[vault.String()] = true
	}
	checkStrings := func(path string, value reflect.Value) {
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if field.Type.Kind() == reflect.String && field.Type.Name() != "TreasuryVault" {
				if !dictionary[value.Field(i).String()] {
					t.Fatalf("%s.%s carries %q outside the closed dictionary", path, field.Name, value.Field(i).String())
				}
			}
		}
	}

	snapshotType := reflect.TypeOf(application.CustodyTotalsSnapshot{})
	for i := 0; i < snapshotType.NumField(); i++ {
		field := snapshotType.Field(i)
		lowered := strings.ToLower(field.Name)
		for _, marker := range []string{"account", "beneficiary", "transfer", "governor", "approver", "email", "secret", "token", "holder", "person", "key", "identifier", "address", "purpose"} {
			if strings.Contains(lowered, marker) {
				t.Fatalf("Snapshot.%s names %q", field.Name, marker)
			}
		}
		switch field.Type.Kind() {
		case reflect.Int, reflect.Int64, reflect.Bool:
		case reflect.String:
			if field.Name != "Locale" {
				t.Fatalf("Snapshot.%s is a free string; titles live in Labels", field.Name)
			}
		case reflect.Struct:
			switch field.Type {
			case reflect.TypeOf(domain.TotalsLabels{}):
				checkStrings("Labels", reflect.ValueOf(domain.TotalsLabelsFor(domain.TotalsLocalePortuguese)))
				checkStrings("Labels", reflect.ValueOf(domain.TotalsLabelsFor(domain.TotalsLocaleEnglish)))
			case reflect.TypeOf(time.Time{}):
			default:
				t.Fatalf("Snapshot.%s has non-instant struct type %s", field.Name, field.Type)
			}
		case reflect.Slice:
			if field.Type != reflect.TypeOf([]application.VaultTotal{}) {
				t.Fatalf("Snapshot.%s holds %s; only vault totals may repeat", field.Name, field.Type)
			}
		default:
			t.Fatalf("Snapshot.%s has kind %s; only integers, flags, instants, titles and vaults may serialize", field.Name, field.Type.Kind())
		}
	}
	for _, vault := range domain.AllTreasuryVaults() {
		if !vaults[vault.String()] {
			t.Fatalf("vault %q outside the closed vocabulary", vault)
		}
	}
	vaultTotalType := reflect.TypeOf(application.VaultTotal{})
	if vaultTotalType.NumField() != 2 {
		t.Fatalf("VaultTotal has %d fields, want Vault and Millis only", vaultTotalType.NumField())
	}
	for i := 0; i < vaultTotalType.NumField(); i++ {
		field := vaultTotalType.Field(i)
		switch field.Name {
		case "Vault":
			if field.Type != reflect.TypeOf(domain.TreasuryVault("")) {
				t.Fatalf("VaultTotal.Vault is %s, want the closed vault vocabulary", field.Type)
			}
		case "Millis":
			if field.Type.Kind() != reflect.Int64 {
				t.Fatalf("VaultTotal.Millis is %s, want int64", field.Type.Kind())
			}
		default:
			t.Fatalf("VaultTotal.%s is not Vault or Millis", field.Name)
		}
		lowered := strings.ToLower(field.Name)
		for _, marker := range []string{"label", "account", "id"} {
			if strings.Contains(lowered, marker) {
				t.Fatalf("VaultTotal.%s names %q", field.Name, marker)
			}
		}
	}
}
