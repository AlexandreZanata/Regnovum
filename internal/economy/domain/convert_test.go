package domain_test

import (
	"errors"
	"math"
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/economy/domain"
)

func TestLegacyUnitsOfRequiresWholeUnits(t *testing.T) {
	t.Parallel()

	units, err := domain.LegacyUnitsOf(mustConvertMilli(t, 5000))
	if err != nil || units != 5 {
		t.Fatalf("LegacyUnitsOf(5000) = %d, %v; want 5, nil", units, err)
	}
	if _, err := domain.LegacyUnitsOf(mustConvertMilli(t, 1500)); !errors.Is(err, domain.ErrInvalidCharter) {
		t.Fatalf("LegacyUnitsOf(1500) = %v, want ErrInvalidCharter", err)
	}
}

func TestConvertedMillisPricesExactly(t *testing.T) {
	t.Parallel()

	converted, err := domain.ConvertedMillis(5, mustConvertRate(t, 1000, 1))
	if err != nil || converted.Millis() != 5000 {
		t.Fatalf("ConvertedMillis(5, 1000/1) = %v, %v; want 5000, nil", converted.Millis(), err)
	}
	floored, err := domain.ConvertedMillis(7, mustConvertRate(t, 1, 3))
	if err != nil || floored.Millis() != 2 {
		t.Fatalf("ConvertedMillis(7, 1/3) = %v, %v; want 2, nil", floored.Millis(), err)
	}
	if _, err := domain.ConvertedMillis(1, mustConvertRate(t, 1, 3)); !errors.Is(err, domain.ErrInvalidCharter) {
		t.Fatalf("dust-only conversion = %v, want ErrInvalidCharter", err)
	}
	if _, err := domain.ConvertedMillis(math.MaxInt64, mustConvertRate(t, 2, 1)); !errors.Is(err, domain.ErrMilliInkOverflow) {
		t.Fatalf("overflowing price = %v, want ErrMilliInkOverflow", err)
	}
	for _, units := range []int64{0, -3} {
		if _, err := domain.ConvertedMillis(units, mustConvertRate(t, 1, 1)); !errors.Is(err, domain.ErrInvalidCharter) {
			t.Fatalf("ConvertedMillis(%d) = %v, want ErrInvalidCharter", units, err)
		}
	}
}

func mustConvertMilli(t *testing.T, millis int64) domain.MilliInk {
	t.Helper()
	amount, err := domain.NewMilliInk(millis)
	if err != nil {
		t.Fatalf("NewMilliInk(%d): %v", millis, err)
	}
	return amount
}

func mustConvertRate(t *testing.T, num, den int64) domain.ConversionRate {
	t.Helper()
	rate, err := domain.ParseConversionRate(num, den)
	if err != nil {
		t.Fatalf("ParseConversionRate(%d, %d): %v", num, den, err)
	}
	return rate
}
