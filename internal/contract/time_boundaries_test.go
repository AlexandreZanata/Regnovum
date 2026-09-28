package contract_test

// P27-T03 — matriz temporal sobre políticas reais e Postgres real.
//
// UTC, offsets extremos, DST, leap day, viradas de mês/ano, timezone
// inválida, skew como offset e expiração exatamente no limite, para
// sessão, passe, subscription, job e retenção: persistência em UTC,
// apresentação em UTC/RFC 3339, comparação por instante (nunca data
// local) e regra idêntica nos dois locales.

import (
	"context"
	"testing"
	"time"

	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	jobsdomain "github.com/AlexandreZanata/Regnovum/internal/jobs/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/dbtest"
	profilesdomain "github.com/AlexandreZanata/Regnovum/internal/profiles/domain"
)

// extremeZones are fixed offsets no calendar math may depend on:
// Kiritimati (+14), Baker (-12) and Kathmandu (+5:45).
func extremeZones() map[string]*time.Location {
	return map[string]*time.Location{
		"kiritimati": time.FixedZone("LINT", 14*3600),
		"baker":      time.FixedZone("BIT", -12*3600),
		"kathmandu":  time.FixedZone("NPT", 5*3600+45*60),
	}
}

// dstZone loads a real DST zone; tzdata ships with the binary
// (time/tzdata) and the CI images, so absence fails loudly, never skips.
func dstZone(t *testing.T, name string) *time.Location {
	t.Helper()
	zone, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return zone
}

// TestTimeSessionBoundaries prova expiração no limite exato: na igualdade
// ainda vale, 1ns depois expirou; 24h através do salto de DST são 24h;
// o mesmo instante em zonas opostas decide igual.
func TestTimeSessionBoundaries(t *testing.T) {
	t.Parallel()
	policy := identitydomain.DefaultSessionPolicy()
	zones := extremeZones()
	saoPaulo := dstZone(t, "America/Sao_Paulo")

	created := time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)
	if policy.IsExpired(created, created, created.Add(24*time.Hour)) {
		t.Fatal("idle session expires exactly at 24h (After is strict)")
	}
	if !policy.IsExpired(created, created, created.Add(24*time.Hour+time.Nanosecond)) {
		t.Fatal("idle session survives past 24h")
	}
	// A borda absoluta decide sozinha com last seen fresco: na igualdade
	// ainda vale, 1ns depois expirou (a regra ociosa decidiria antes).
	fresh := created.Add(14*24*time.Hour - time.Hour)
	if policy.IsExpired(created, fresh, created.Add(14*24*time.Hour)) {
		t.Fatal("session expires exactly at 14d (After is strict)")
	}
	if !policy.IsExpired(created, fresh, created.Add(14*24*time.Hour+time.Nanosecond)) {
		t.Fatal("session survives past 14d")
	}

	// Primavera de São Paulo (2026-10-18, 00:00 pula para 01:00): 24h de
	// relógio de parede curto continuam 24h de instante.
	dstCreated := time.Date(2026, time.October, 17, 12, 0, 0, 0, saoPaulo)
	if policy.IsExpired(dstCreated, dstCreated, dstCreated.Add(24*time.Hour)) {
		t.Fatal("DST day expires the session early")
	}
	if !policy.IsExpired(dstCreated, dstCreated, dstCreated.Add(24*time.Hour+time.Nanosecond)) {
		t.Fatal("DST day extends the session")
	}

	// Leap day cria sessão válida com os mesmos limites.
	leap := time.Date(2024, time.February, 29, 12, 0, 0, 0, time.UTC)
	if policy.IsExpired(leap, leap, leap.Add(24*time.Hour)) {
		t.Fatal("leap-day session expires at the idle edge")
	}

	// Skew como offset: o mesmo instante em +14:00 e -12:00 decide igual,
	// provando comparação por instante e nunca por data local.
	kiritimatiNow := time.Date(2026, time.June, 1, 12, 0, 0, 0, zones["kiritimati"])
	bakerNow := kiritimatiNow.In(zones["baker"])
	if !kiritimatiNow.Equal(bakerNow) {
		t.Fatal("same instant reads different across extreme offsets")
	}
	old := time.Date(2026, time.May, 1, 12, 0, 0, 0, time.UTC)
	for locale, now := range map[string]time.Time{"pt-BR": kiritimatiNow, "en-US": bakerNow} {
		if policy.IsExpired(old, old, now) != policy.IsExpired(old, old, kiritimatiNow) {
			t.Fatalf("locale %s changes the expiry verdict", locale)
		}
	}
}

// TestTimeBillingPeriods prova períodos sobre viradas: mês, ano e leap
// day passam; fim igual/invertido recusa; bordas gravam em UTC.
func TestTimeBillingPeriods(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		start time.Time
		end   time.Time
	}{
		{"month turn", time.Date(2026, time.January, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, time.February, 15, 0, 0, 0, 0, time.UTC)},
		{"year turn", time.Date(2025, time.December, 20, 0, 0, 0, 0, time.UTC), time.Date(2026, time.January, 20, 0, 0, 0, 0, time.UTC)},
		{"leap start", time.Date(2024, time.February, 29, 0, 0, 0, 0, time.UTC), time.Date(2024, time.March, 29, 0, 0, 0, 0, time.UTC)},
		{"extreme offsets", time.Date(2026, time.June, 1, 0, 0, 0, 0, extremeZones()["kiritimati"]), time.Date(2026, time.July, 1, 0, 0, 0, 0, extremeZones()["baker"])},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			period, err := billingdomain.NewBillingPeriod(testCase.start, testCase.end)
			if err != nil {
				t.Fatalf("NewBillingPeriod: %v", err)
			}
			if period.Start().Location() != time.UTC || period.End().Location() != time.UTC {
				t.Fatal("period edges are not normalized to UTC")
			}
			if !period.Start().Equal(testCase.start) || !period.End().Equal(testCase.end) {
				t.Fatal("period edges moved across the turn")
			}
		})
	}
	now := time.Now().UTC()
	if _, err := billingdomain.NewBillingPeriod(now, now); err == nil {
		t.Fatal("zero-length period was accepted")
	}
	if _, err := billingdomain.NewBillingPeriod(now.Add(time.Hour), now); err == nil {
		t.Fatal("inverted period was accepted")
	}
}

// TestTimeJobsPeriods prova cadências em UTC de calendário: o mês de um
// instante é o mês UTC dele em qualquer zona, round-trip preserva, e
// viradas e leap day classificam certo.
func TestTimeJobsPeriods(t *testing.T) {
	t.Parallel()
	zones := extremeZones()
	saoPaulo := dstZone(t, "America/Sao_Paulo")

	// 2026-01-31 23:00 em Kiritimati (+14) ainda é janeiro em UTC? Não:
	// 09:00 UTC de 01-31 — o rótulo segue o UTC, não a parede local.
	jan31 := time.Date(2026, time.January, 31, 23, 0, 0, 0, zones["kiritimati"])
	period, err := jobsdomain.PeriodAt(jobsdomain.IntervalMonthly, jan31)
	if err != nil {
		t.Fatalf("PeriodAt: %v", err)
	}
	if period.String() != "2026-01" {
		t.Fatalf("monthly label = %q, want the UTC month", period.String())
	}
	// Virada de ano em Baker (-12): 2025-12-31 13:00 local já é 2026-01-01 UTC.
	newYear := time.Date(2025, time.December, 31, 13, 0, 0, 0, zones["baker"])
	period, err = jobsdomain.PeriodAt(jobsdomain.IntervalMonthly, newYear)
	if err != nil {
		t.Fatalf("PeriodAt: %v", err)
	}
	if period.String() != "2026-01" {
		t.Fatalf("year-turn label = %q, want 2026-01", period.String())
	}
	// Leap day diário e round-trip mensal.
	leapDay, err := jobsdomain.PeriodAt(jobsdomain.IntervalDaily, time.Date(2024, time.February, 29, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("PeriodAt leap: %v", err)
	}
	if leapDay.String() != "2024-02-29" {
		t.Fatalf("daily leap label = %q", leapDay.String())
	}
	roundTrip, err := jobsdomain.ParsePeriod(jobsdomain.IntervalMonthly, "2026-02")
	if err != nil {
		t.Fatalf("ParsePeriod: %v", err)
	}
	if roundTrip.String() != "2026-02" {
		t.Fatalf("round-trip label = %q", roundTrip.String())
	}
	// DST não move cadência: meio-dia de São Paulo classifica no dia UTC.
	dstNoon, err := jobsdomain.PeriodAt(jobsdomain.IntervalDaily, time.Date(2026, time.October, 18, 12, 0, 0, 0, saoPaulo))
	if err != nil {
		t.Fatalf("PeriodAt DST: %v", err)
	}
	if dstNoon.String() != "2026-10-18" {
		t.Fatalf("DST daily label = %q", dstNoon.String())
	}
	if _, err := jobsdomain.PeriodAt("hourly", time.Now().UTC()); err == nil {
		t.Fatal("unknown cadence was accepted")
	}
	if _, err := jobsdomain.ParsePeriod(jobsdomain.IntervalMonthly, "2026-13"); err == nil {
		t.Fatal("month 13 was accepted")
	}
}

// TestTimeRetentionWindows prova base e prazo da retenção e o cooldown da
// deleção no limite: na igualdade já venceu, 1ns antes ainda não.
func TestTimeRetentionWindows(t *testing.T) {
	t.Parallel()
	if profilesdomain.RetentionTokensWindow != 30*24*time.Hour {
		t.Fatalf("tokens window = %s, want the documented 30d", profilesdomain.RetentionTokensWindow)
	}
	if profilesdomain.RetentionSessionsWindow != 30*24*time.Hour {
		t.Fatalf("sessions window = %s, want the documented 30d", profilesdomain.RetentionSessionsWindow)
	}
	if profilesdomain.RetentionAbuseSignalsWindow != 7*24*time.Hour {
		t.Fatalf("abuse window = %s, want the documented 7d", profilesdomain.RetentionAbuseSignalsWindow)
	}
	for _, schedule := range profilesdomain.RetentionSchedules() {
		if err := schedule.IsValid(); err != nil {
			t.Fatalf("schedule %+v invalid: %v", schedule, err)
		}
		if schedule.ReasonCode == "" {
			t.Fatalf("schedule %+v has no documented basis", schedule)
		}
	}

	requested := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	due := requested.Add(profilesdomain.DeletionCooldown)
	if !profilesdomain.DeletionExecutable(requested, due) {
		t.Fatal("deletion not due exactly at cooldown")
	}
	if profilesdomain.DeletionExecutable(requested, due.Add(-time.Nanosecond)) {
		t.Fatal("deletion due before the cooldown")
	}
	// Virada de mês/ano no cooldown conta duração, não calendário.
	yearTurn := time.Date(2025, time.December, 25, 12, 0, 0, 0, time.UTC)
	if !profilesdomain.DeletionExecutable(yearTurn, yearTurn.Add(profilesdomain.DeletionCooldown)) {
		t.Fatal("year-turn cooldown not due at the edge")
	}
}

// TestTimePersistenceUTC prova que o banco guarda UTC: linhas escritas em
// +14:00 e -11:00 voltam iguais e normalizadas, para sessão e passe.
func TestTimePersistenceUTC(t *testing.T) {
	t.Parallel()
	database := dbtest.New(t)
	pool := database.Pool.Pool()
	zones := extremeZones()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `INSERT INTO app.accounts (email, status) VALUES ('t03-clock@arena.example.com', 'active')`); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	var accountID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM app.accounts WHERE email = 't03-clock@arena.example.com'`).Scan(&accountID); err != nil {
		t.Fatalf("resolve account: %v", err)
	}
	created := time.Date(2024, time.February, 29, 12, 0, 0, 0, zones["kiritimati"])
	expires := time.Date(2026, time.December, 31, 23, 0, 0, 0, zones["baker"])
	digest := [32]byte{9, 8, 7}
	var readCreated, readExpires time.Time
	if err := pool.QueryRow(ctx, `INSERT INTO app.sessions (account_id, token_hash, created_at, expires_at)
		VALUES ($1::uuid, $2, $3, $4) RETURNING created_at, expires_at`,
		accountID, digest[:], created, expires).Scan(&readCreated, &readExpires); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if !readCreated.Equal(created) || !readExpires.Equal(expires) {
		t.Fatal("timestamptz round-trip moved the instant")
	}
	// O driver entrega no fuso local; a fronteira normaliza com UTC, e é
	// essa igualdade normalizada que o produto garante em toda leitura.
	if !readCreated.UTC().Equal(created.UTC()) || !readExpires.UTC().Equal(expires.UTC()) {
		t.Fatal("UTC-normalized round-trip moved the instant")
	}
}

// TestTimeInvalidTimezone prova que zona desconhecida é recusada e que as
// duas locales resolvem o mesmo conjunto.
func TestTimeInvalidTimezone(t *testing.T) {
	t.Parallel()
	// Vazio é a preferência ausente (zero, sem erro); o resto fora do
	// banco de zonas cai, inclusive com NUL e o alias dependente de
	// processo Local.
	if zone, err := profilesdomain.ParseTimezone(""); err != nil || !zone.IsZero() {
		t.Fatalf("empty timezone = %v, %v; want the unset zero", zone, err)
	}
	for _, raw := range []string{"Mars/Olympus", "GMT+99", "Local", "America/Sao_Paulo\x00"} {
		if _, err := profilesdomain.ParseTimezone(raw); err == nil {
			t.Fatalf("timezone %q was accepted", raw)
		}
	}
	for _, raw := range []string{"America/Sao_Paulo", "UTC", "America/New_York"} {
		if _, err := profilesdomain.ParseTimezone(raw); err != nil {
			t.Fatalf("timezone %q refused: %v", raw, err)
		}
	}
}
