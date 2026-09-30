package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/billing/domain"
)

func TestPassOriginVocabulary(t *testing.T) {
	expected := []string{"PURCHASE", "MEMBER", "ADMIN"}
	all := domain.AllPassOrigins()
	if len(all) != len(expected) {
		t.Fatalf("AllPassOrigins() has %d entries, want %d", len(all), len(expected))
	}
	for i, raw := range expected {
		if all[i].String() != raw {
			t.Errorf("AllPassOrigins()[%d] = %q, want %q", i, all[i], raw)
		}
		parsed, err := domain.ParsePassOrigin(raw)
		if err != nil {
			t.Fatalf("ParsePassOrigin(%q) error = %v", raw, err)
		}
		if parsed != all[i] || !parsed.IsValid() {
			t.Errorf("ParsePassOrigin(%q) = %q/%v", raw, parsed, parsed.IsValid())
		}
	}

	for _, raw := range []string{"", "GIFT", "purchase", "MEMBER "} {
		if _, err := domain.ParsePassOrigin(raw); !errors.Is(err, domain.ErrInvalidPassOrigin) {
			t.Errorf("ParsePassOrigin(%q) error = %v, want ErrInvalidPassOrigin", raw, err)
		}
	}
}

func TestQuantityValueObject(t *testing.T) {
	for _, amount := range []int32{1, 5, 100} {
		quantity, err := domain.NewQuantity(amount)
		if err != nil {
			t.Fatalf("NewQuantity(%d) error = %v", amount, err)
		}
		if quantity.Int32() != amount || quantity.IsZero() {
			t.Errorf("NewQuantity(%d) = %d/%v", amount, quantity.Int32(), quantity.IsZero())
		}
	}

	for _, amount := range []int32{0, -1, -100} {
		quantity, err := domain.NewQuantity(amount)
		if !errors.Is(err, domain.ErrInvalidQuantity) {
			t.Fatalf("NewQuantity(%d) error = %v, want ErrInvalidQuantity", amount, err)
		}
		if !quantity.IsZero() {
			t.Fatal("failed quantity must be the zero value")
		}
	}

	var zero domain.Quantity
	if !zero.IsZero() || zero.String() != "0" {
		t.Error("zero Quantity must report zero and render 0")
	}
	left, _ := domain.NewQuantity(3)
	same, _ := domain.NewQuantity(3)
	other, _ := domain.NewQuantity(4)
	if !left.Equals(same) || left.Equals(other) || left.Equals(zero) {
		t.Error("quantity equality semantics are inconsistent")
	}
	if left.String() != "3" {
		t.Errorf("String() = %q, want 3", left.String())
	}
}

func TestPassReferenceValueObject(t *testing.T) {
	valid := []struct {
		input string
		want  string
	}{
		{input: "stripe:evt_1Pabcdef", want: "stripe:evt_1Pabcdef"},
		{input: "member:2026-09:018f6b2a", want: "member:2026-09:018f6b2a"},
		{input: "  admin:ticket-9  ", want: "admin:ticket-9"},
		{input: strings.Repeat("r", 200), want: strings.Repeat("r", 200)},
		{input: "edge!~ref", want: "edge!~ref"},
	}
	for _, tc := range valid {
		reference, err := domain.ParseReference(tc.input)
		if err != nil {
			t.Fatalf("ParseReference(%q) error = %v", tc.input, err)
		}
		if reference.String() != tc.want || reference.IsZero() {
			t.Errorf("ParseReference(%q) = %q", tc.input, reference.String())
		}
	}

	invalid := []struct {
		name  string
		input string
		want  error
	}{
		{name: "empty", input: "", want: domain.ErrEmptyReference},
		{name: "blank", input: "   ", want: domain.ErrEmptyReference},
		{name: "too long", input: strings.Repeat("r", 201), want: domain.ErrReferenceTooLong},
		{name: "inner space", input: "stripe: evt", want: domain.ErrInvalidReference},
		{name: "inner DEL", input: "stripe:\x7f64", want: domain.ErrInvalidReference},
		{name: "newline", input: "stripe:\nevt", want: domain.ErrInvalidReference},
		{name: "non-ascii", input: "stripe:ção", want: domain.ErrInvalidReference},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			reference, err := domain.ParseReference(tc.input)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseReference(%q) error = %v, want %v", tc.input, err, tc.want)
			}
			if !reference.IsZero() {
				t.Fatal("failed parse must yield the zero reference")
			}
		})
	}
}

func mustPassLot(t *testing.T, expiresAt *time.Time) *domain.PassLot {
	t.Helper()
	quantity, err := domain.NewQuantity(3)
	if err != nil {
		t.Fatalf("NewQuantity: %v", err)
	}
	reference, err := domain.ParseReference("stripe:evt_test")
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	lot, err := domain.ReconstitutePassLot(
		domain.LotID("lot-1"),
		domain.AccountID("018f6b2a-0000-7000-8000-000000000001"),
		domain.OriginPurchase,
		quantity,
		3,
		expiresAt,
		reference,
		time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("ReconstitutePassLot: %v", err)
	}
	return lot
}

func TestReconstitutePassLot(t *testing.T) {
	expiry := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	lot := mustPassLot(t, &expiry)

	if lot.ID().String() != "lot-1" || lot.AccountID().String() == "" {
		t.Errorf("identifiers = %q/%q", lot.ID(), lot.AccountID())
	}
	if lot.Origin() != domain.OriginPurchase || lot.Quantity().Int32() != 3 || lot.Remaining() != 3 {
		t.Errorf("lot = %s q%d r%d", lot.Origin(), lot.Quantity().Int32(), lot.Remaining())
	}
	if lot.Reference().String() != "stripe:evt_test" {
		t.Errorf("Reference() = %q", lot.Reference())
	}
	if lot.ExpiresAt() == nil || !lot.ExpiresAt().Equal(expiry) {
		t.Errorf("ExpiresAt() = %v, want %v", lot.ExpiresAt(), expiry)
	}
	if !lot.CreatedAt().Equal(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("CreatedAt() = %v", lot.CreatedAt())
	}

	// Expiration copies are defensive and always UTC.
	zoned := time.Date(2026, 10, 1, 3, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	zonedLot := mustPassLot(t, &zoned)
	if zonedLot.ExpiresAt().Location() != time.UTC {
		t.Errorf("expiration location = %v, want UTC", zonedLot.ExpiresAt().Location())
	}
	if !zonedLot.ExpiresAt().Equal(zoned) {
		t.Errorf("expiration instant drifted: %v vs %v", zonedLot.ExpiresAt(), zoned)
	}
	first := zonedLot.ExpiresAt()
	*first = first.Add(time.Hour)
	if !zonedLot.ExpiresAt().Equal(zoned) {
		t.Error("ExpiresAt must return a copy, not the internal instant")
	}
}

func TestReconstitutePassLotValidatesInvariants(t *testing.T) {
	quantity, _ := domain.NewQuantity(2)
	reference, _ := domain.ParseReference("stripe:evt_invariants")
	accountID := domain.AccountID("018f6b2a-0000-7000-8000-000000000002")

	tests := []struct {
		name      string
		id        domain.LotID
		accountID domain.AccountID
		origin    domain.PassOrigin
		quantity  domain.Quantity
		remaining int32
		reference domain.Reference
		want      error
	}{
		{name: "empty id", id: "", accountID: accountID, origin: domain.OriginPurchase, quantity: quantity, remaining: 2, reference: reference, want: domain.ErrEmptyLotID},
		{name: "empty account", id: "lot-1", accountID: "", origin: domain.OriginPurchase, quantity: quantity, remaining: 2, reference: reference, want: domain.ErrEmptyAccountID},
		{name: "invalid origin", id: "lot-1", accountID: accountID, origin: domain.PassOrigin("GIFT"), quantity: quantity, remaining: 2, reference: reference, want: domain.ErrInvalidPassOrigin},
		{name: "zero quantity", id: "lot-1", accountID: accountID, origin: domain.OriginPurchase, quantity: domain.Quantity{}, remaining: 0, reference: reference, want: domain.ErrInvalidQuantity},
		{name: "negative remaining", id: "lot-1", accountID: accountID, origin: domain.OriginPurchase, quantity: quantity, remaining: -1, reference: reference, want: domain.ErrInvalidRemaining},
		{name: "remaining above quantity", id: "lot-1", accountID: accountID, origin: domain.OriginPurchase, quantity: quantity, remaining: 3, reference: reference, want: domain.ErrInvalidRemaining},
		{name: "empty reference", id: "lot-1", accountID: accountID, origin: domain.OriginPurchase, quantity: quantity, remaining: 2, reference: domain.Reference{}, want: domain.ErrEmptyReference},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lot, err := domain.ReconstitutePassLot(
				tc.id, tc.accountID, tc.origin, tc.quantity, tc.remaining, nil, tc.reference, time.Now(),
			)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if lot != nil {
				t.Fatal("expected nil lot on error")
			}
		})
	}
}

func TestPassLotExpirationAndAvailability(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	neverExpires := mustPassLot(t, nil)
	if neverExpires.IsExpired(now) || !neverExpires.IsAvailable(now) {
		t.Error("a lot without expiration must stay available")
	}

	expiry := now.Add(time.Hour)
	expiring := mustPassLot(t, &expiry)
	if expiring.IsExpired(now) || !expiring.IsAvailable(now) {
		t.Error("a lot before its expiration must be available")
	}
	if !expiring.IsExpired(expiry) {
		t.Error("the expiration instant itself must already be expired")
	}
	if expiring.IsAvailable(expiry.Add(time.Nanosecond)) {
		t.Error("an expired lot must not be available, even with passes left")
	}

	// Consumption boundary: zero remaining is never available.
	quantity, _ := domain.NewQuantity(1)
	reference, _ := domain.ParseReference("stripe:evt_zero")
	consumed, err := domain.ReconstitutePassLot(
		domain.LotID("lot-zero"),
		domain.AccountID("018f6b2a-0000-7000-8000-000000000003"),
		domain.OriginPurchase,
		quantity,
		0,
		nil,
		reference,
		now,
	)
	if err != nil {
		t.Fatalf("ReconstitutePassLot: %v", err)
	}
	if consumed.IsAvailable(now) {
		t.Error("a fully consumed lot must not be available")
	}
}
