package main

import (
	"strings"
	"testing"
)

func cleanSnapshot() snapshot {
	return snapshot{
		Tables: map[string]int64{
			"app.wallet_accounts": 2, "app.wallet_operations": 3, "app.wallet_transactions": 4,
			"app.arena_pass_lots": 2, "app.arena_pass_consumptions": 1,
			"app.stripe_customers": 1, "app.subscriptions": 1,
		},
		Buckets: []bucketTotal{
			{Bucket: "FREE_INK", Operations: 2, LedgerSum: 8000, Projected: 8000, Holders: 2},
			{Bucket: "PURCHASED_INK", Operations: 2, LedgerSum: 40000, Projected: 40000, Holders: 1},
		},
		Operations: []operationTotal{
			{Operation: "credit_free", Count: 1},
			{Operation: "credit_purchase", Count: 1},
			{Operation: "debit_argument", Count: 1},
		},
		Passes: []passTotal{
			{Origin: "PURCHASE", Lots: 1, Quantity: 5, Remaining: 4, Consumed: 1, Holders: 1},
			{Origin: "MEMBER", Lots: 1, Quantity: 1, Remaining: 1, Consumed: 0, Expired: 1, Holders: 1},
		},
		Subscriptions: []subscriptionTotal{
			{Status: "active", Count: 1},
		},
		Orphans: map[string]int64{
			"wallet_without_account": 0, "operation_without_wallet": 0, "transaction_without_operation": 0,
			"lot_without_account": 0, "consumption_without_lot": 0,
			"subscription_without_customer": 0, "customer_without_account": 0,
		},
		Unknowns: map[string]int64{
			"unknown_bucket": 0, "unknown_operation": 0, "unknown_origin": 0, "unknown_status": 0,
		},
	}
}

// TestClassifyCleanSnapshotHolds proves a reconciled book reports no
// finding: every contract classified, every sum matched.
func TestClassifyCleanSnapshotHolds(t *testing.T) {
	t.Parallel()

	if findings := classify(cleanSnapshot()); len(findings) != 0 {
		t.Fatalf("clean snapshot findings = %v", findings)
	}
	contracts := summarize(cleanSnapshot())
	if len(contracts) != 5 {
		t.Fatalf("contracts = %d, want franchise, purchased-ink, 2 pass origins and 1 subscription", len(contracts))
	}
	byContract := map[string]ContractSummary{}
	for _, contract := range contracts {
		byContract[contract.Contract+"/"+contract.Origin] = contract
	}
	franchise, ok := byContract["franchise/plan"]
	if !ok || franchise.Total != 8000 || franchise.Obligation != "non-refundable" || franchise.Term != "period" {
		t.Errorf("franchise = %+v", franchise)
	}
	purchased, ok := byContract["purchased-ink/stripe"]
	if !ok || purchased.Total != 40000 || purchased.Obligation != "refundable-unused" {
		t.Errorf("purchased = %+v", purchased)
	}
	member, ok := byContract["arena-pass/member"]
	if !ok || member.Term != "period" || member.Total != 1 {
		t.Errorf("member pass = %+v", member)
	}
}

// TestClassifyRefusesOrphans proves every orphan class blocks: one
// unattributed row anywhere fails the whole inventory.
func TestClassifyRefusesOrphans(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"wallet_without_account", "operation_without_wallet", "transaction_without_operation",
		"lot_without_account", "consumption_without_lot",
		"subscription_without_customer", "customer_without_account",
	} {
		snap := cleanSnapshot()
		snap.Orphans[name] = 1
		findings := classify(snap)
		if len(findings) != 1 || !strings.HasPrefix(findings[0].Rule, "orphan-") {
			t.Errorf("%s: findings = %v, want one orphan refusal", name, findings)
		}
	}
}

// TestClassifyRefusesAmbiguity proves values outside every closed
// vocabulary, negative balances, zero lines, over-consumption and both
// arithmetic mismatches each fail exactly their own rule.
func TestClassifyRefusesAmbiguity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*snapshot)
		rule   string
	}{
		{"unknown bucket", func(s *snapshot) { s.Unknowns["unknown_bucket"] = 2 }, "unknown_bucket"},
		{"unknown operation", func(s *snapshot) { s.Unknowns["unknown_operation"] = 1 }, "unknown_operation"},
		{"unknown origin", func(s *snapshot) { s.Unknowns["unknown_origin"] = 1 }, "unknown_origin"},
		{"unknown status", func(s *snapshot) { s.Unknowns["unknown_status"] = 1 }, "unknown_status"},
		{"negative wallet", func(s *snapshot) { s.NegativeWallets = 1 }, "negative-balance"},
		{"zero amount", func(s *snapshot) { s.ZeroAmounts = 3 }, "zero-amount"},
		{"over consumed", func(s *snapshot) { s.OverConsumed = 1 }, "over-consumed"},
		{"ledger drift", func(s *snapshot) { s.Buckets[0].Projected++ }, "ledger-projection-mismatch"},
		{"lot drift", func(s *snapshot) { s.Passes[0].Consumed++ }, "lot-arithmetic-mismatch"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			snap := cleanSnapshot()
			test.mutate(&snap)
			findings := classify(snap)
			if len(findings) != 1 || findings[0].Rule != test.rule {
				t.Fatalf("findings = %v, want exactly rule %q", findings, test.rule)
			}
		})
	}
}

// TestReportCarriesNoPII proves the encoding holds aggregates only:
// identifiers and emails cannot leak because they never enter it.
func TestReportCarriesNoPII(t *testing.T) {
	t.Parallel()

	encoded, err := Render(Report{
		GeneratedAt: "2026-09-29T00:00:00Z",
		Tables:      map[string]int64{"app.wallet_accounts": 1},
		Contracts: []ContractSummary{{
			Contract: "franchise", Origin: "plan", Term: "period",
			Obligation: "non-refundable", Rows: 1, Total: 5000, Accounts: 1,
		}},
		Findings: []Finding{},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, forbidden := range []string{"@", "example.com", "cus_", "sub_", "evt_"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("report contains %q: identifiers leak into the encoding", forbidden)
		}
	}
}
