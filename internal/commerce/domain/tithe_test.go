package domain

import (
	"errors"
	"testing"
)

func TestSplitTitheCoversZeroToTwentyAndThousand(t *testing.T) {
	cases := []struct {
		amount int64
		tithe  int64
		net    int64
	}{
		{1, 0, 1},
		{2, 0, 2},
		{9, 0, 9},
		{10, 1, 10 - 1},
		{11, 1, 10},
		{19, 1, 18},
		{20, 2, 18},
		{1000, 100, 900},
		{20000, 2000, 18000},
	}
	for _, tc := range cases {
		tithe, net, err := SplitTithe(tc.amount)
		if err != nil {
			t.Fatalf("SplitTithe(%d): %v", tc.amount, err)
		}
		if tithe != tc.tithe || net != tc.net {
			t.Fatalf("SplitTithe(%d) = (%d,%d), want (%d,%d)",
				tc.amount, tithe, net, tc.tithe, tc.net)
		}
		if tithe+net != tc.amount {
			t.Fatalf("SplitTithe(%d): outputs %d+%d != paid %d",
				tc.amount, tithe, net, tc.amount)
		}
	}
	for amount := int64(0); amount <= 20; amount++ {
		if amount <= 0 {
			continue
		}
		tithe, net, err := SplitTithe(amount)
		if err != nil {
			t.Fatalf("SplitTithe(%d): %v", amount, err)
		}
		if tithe != amount/10 || net != amount-tithe {
			t.Fatalf("SplitTithe(%d) = (%d,%d), want floor 10%%", amount, tithe, net)
		}
	}
}

func TestSplitTitheRefusesNonPositive(t *testing.T) {
	for _, amount := range []int64{0, -1, -100} {
		if _, _, err := SplitTithe(amount); !errors.Is(err, ErrInvalidContract) {
			t.Fatalf("SplitTithe(%d) = %v, want ErrInvalidContract", amount, err)
		}
	}
}
