package domain

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestRealRefluxIndexAllowsAboveOneHundred(t *testing.T) {
	returns := []RefluxLeg{
		{RefluxPublication, "credit", 1000},
		{RefluxPublication, "credit", 1000},
	}
	outflows := []OutflowLeg{{OutflowCrumbDistribution, 1000}}
	report, err := RealRefluxIndex(returns, outflows)
	if err != nil {
		t.Fatalf("RealRefluxIndex: %v", err)
	}
	if report.Returns != 2000 || report.Outflows != 1000 {
		t.Fatalf("report = %+v, want gross 2000/1000: the same INK circulating twice", report)
	}
	if !report.HasRatio || report.PerMille != 2000 || report.RatioLabel() != "200.0%" {
		t.Fatalf("ratio = %v/%d/%q, want true/2000/200.0%%", report.HasRatio, report.PerMille, report.RatioLabel())
	}
	if report.ReturnsByKind[RefluxServiceCharge] != 2000 {
		t.Fatalf("returns by class = %v, want service_charge 2000 traceable", report.ReturnsByKind)
	}
}

func TestRealRefluxIndexEmptyPeriodReportsNA(t *testing.T) {
	report, err := RealRefluxIndex(nil, nil)
	if err != nil {
		t.Fatalf("RealRefluxIndex: %v", err)
	}
	if report.Returns != 0 || report.Outflows != 0 || report.HasRatio {
		t.Fatalf("empty report = %+v, want defined zeros without ratio", report)
	}
	if report.RatioLabel() != "N/A" {
		t.Fatalf("empty label = %q, want N/A: zero denominator never divides", report.RatioLabel())
	}
	funded, err := RealRefluxIndex([]RefluxLeg{{RefluxPublication, "credit", 500}}, nil)
	if err != nil || funded.HasRatio || funded.RatioLabel() != "N/A" {
		t.Fatalf("funded without outflows = %+v/%v, want N/A without error", funded, err)
	}
}

func TestRealRefluxIndexCountsGrossCategoriesOnly(t *testing.T) {
	returns := []RefluxLeg{
		{RefluxPublication, "credit", 2750},
		{RefluxTitheSettlement, "credit", 2000},
		{RefluxMeteringRefund, "debit", 2750},
		{RefluxSale, "credit", 100000},
		{RefluxUnknown, "credit", 100000},
	}
	outflows := []OutflowLeg{
		{OutflowCrumbDistribution, 1000},
		{OutflowSaleSettlement, 2000},
		{OutflowCompensation, 300},
		{OutflowDuePayment, 700},
	}
	report, err := RealRefluxIndex(returns, outflows)
	if err != nil {
		t.Fatalf("RealRefluxIndex: %v", err)
	}
	if report.Returns != 4750 {
		t.Fatalf("returns = %d, want gross 2750+2000: reversals never net the numerator", report.Returns)
	}
	var classSum int64
	for _, amount := range report.ReturnsByKind {
		classSum += amount
	}
	if classSum != report.Returns {
		t.Fatalf("class sum = %d, want %d traceable", classSum, report.Returns)
	}
	if report.Outflows != 4000 {
		t.Fatalf("outflows = %d, want 4000", report.Outflows)
	}
	var kindSum int64
	for _, amount := range report.OutflowsByKind {
		kindSum += amount
	}
	if kindSum != report.Outflows {
		t.Fatalf("kind sum = %d, want %d traceable", kindSum, report.Outflows)
	}
	if len(AllOutflowKinds()) != 4 {
		t.Fatalf("kinds = %d, want the closed four", len(AllOutflowKinds()))
	}
	if _, err := ParseOutflowKind("airdrop"); !errors.Is(err, ErrInvalidMetric) {
		t.Fatalf("unknown kind = %v, want ErrInvalidMetric fail-closed", err)
	}
}

func TestRealRefluxIndexRefusesCheckedArithmetic(t *testing.T) {
	if _, _, err := GrossReturns([]RefluxLeg{{RefluxPublication, "credit", 0}}); !errors.Is(err, ErrInvalidMetric) {
		t.Fatalf("zero amount = %v, want ErrInvalidMetric", err)
	}
	if _, _, err := GrossReturns([]RefluxLeg{
		{RefluxPublication, "credit", math.MaxInt64},
		{RefluxPublication, "credit", 1},
	}); !errors.Is(err, ErrInvalidMetric) {
		t.Fatalf("overflowing returns = %v, want ErrInvalidMetric instead of wrapping", err)
	}
	if _, _, err := SumOutflows([]OutflowLeg{{OutflowKind("airdrop"), 100}}); !errors.Is(err, ErrInvalidMetric) {
		t.Fatalf("unknown outflow = %v, want ErrInvalidMetric", err)
	}
	if _, err := RealRefluxIndex(
		[]RefluxLeg{{RefluxPublication, "credit", math.MaxInt64}},
		[]OutflowLeg{{OutflowCrumbDistribution, 1}},
	); !errors.Is(err, ErrInvalidMetric) {
		t.Fatalf("overflowing ratio = %v, want ErrInvalidMetric instead of wrapping", err)
	}
}

func TestRealRefluxIndexDisclosureIsNotYield(t *testing.T) {
	disclosure := Disclaimer()
	if !strings.Contains(disclosure, MetricName) {
		t.Fatalf("disclosure = %q, want the metric named", disclosure)
	}
	if !strings.Contains(disclosure, "not a yield") {
		t.Fatalf("disclosure = %q, want the number refused as yield", disclosure)
	}
}
