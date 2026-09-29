// Package main snapshots the legacy books (P33-T01): wallet FREE and
// PURCHASED balances, operations and ledger lines, Arena Pass lots and
// consumptions, and Member subscriptions. It reads aggregates only —
// counts and sums grouped by contract, origin and status — so the report
// carries no email, account id or provider identifier. Every aggregate
// is classified against closed vocabularies, and any orphan or
// ambiguous row blocks the migration the report would otherwise allow.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Finding is one violated inventory rule with the evidence that proves it.
type Finding struct {
	Rule   string `json:"rule"`
	Detail string `json:"detail"`
}

// ContractSummary is one classified right: what it is, where it came
// from, how long it lasts, what is owed for it, and how much exists.
type ContractSummary struct {
	Contract   string `json:"contract"`
	Origin     string `json:"origin"`
	Term       string `json:"term"`
	Obligation string `json:"obligation"`
	Rows       int64  `json:"rows"`
	Total      int64  `json:"total"`
	Accounts   int64  `json:"accounts"`
}

// Report is the synthetic-safe inventory of the legacy books.
type Report struct {
	GeneratedAt string            `json:"generated_at"`
	Tables      map[string]int64  `json:"tables"`
	Contracts   []ContractSummary `json:"contracts"`
	Findings    []Finding         `json:"findings"`
}

// bucketTotal is one ledger bucket with its projection beside it.
type bucketTotal struct {
	Bucket     string
	Operations int64
	LedgerSum  int64
	Projected  int64
	Holders    int64
}

// operationTotal is one operation type with its count.
type operationTotal struct {
	Operation string
	Count     int64
}

// passTotal is one pass origin with quantities and expiries.
type passTotal struct {
	Origin    string
	Lots      int64
	Quantity  int64
	Remaining int64
	Consumed  int64
	Expired   int64
	Holders   int64
}

// subscriptionTotal is one provider status with its count.
type subscriptionTotal struct {
	Status string
	Count  int64
}

// snapshot is what the database answered, before classification.
type snapshot struct {
	Tables          map[string]int64
	Buckets         []bucketTotal
	Operations      []operationTotal
	Passes          []passTotal
	Subscriptions   []subscriptionTotal
	Orphans         map[string]int64
	Unknowns        map[string]int64
	NegativeWallets int64
	ZeroAmounts     int64
	OverConsumed    int64
}

// snapshotDatabase reads one disposable database. Every statement is
// SELECT: the tool never writes, migrates or locks beyond a read.
func snapshotDatabase(ctx context.Context, pool *pgxpool.Pool) (snapshot, error) {
	var snap snapshot
	var err error
	if snap.Tables, err = tableCounts(ctx, pool); err != nil {
		return snap, err
	}
	if snap.Buckets, err = bucketTotals(ctx, pool); err != nil {
		return snap, err
	}
	if snap.Operations, err = operationTotals(ctx, pool); err != nil {
		return snap, err
	}
	if snap.Passes, err = passTotals(ctx, pool); err != nil {
		return snap, err
	}
	if snap.Subscriptions, err = subscriptionTotals(ctx, pool); err != nil {
		return snap, err
	}
	if snap.Orphans, err = orphanCounts(ctx, pool); err != nil {
		return snap, err
	}
	if snap.Unknowns, snap.NegativeWallets, snap.ZeroAmounts, snap.OverConsumed, err = shapeViolations(ctx, pool); err != nil {
		return snap, err
	}
	return snap, nil
}

func tableCounts(ctx context.Context, pool *pgxpool.Pool) (map[string]int64, error) {
	tables := map[string]int64{}
	for _, table := range []string{
		"app.wallet_accounts", "app.wallet_operations", "app.wallet_transactions",
		"app.arena_pass_lots", "app.arena_pass_consumptions",
		"app.stripe_customers", "app.subscriptions",
	} {
		var count int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			return nil, fmt.Errorf("count %s: %w", table, err)
		}
		tables[table] = count
	}
	return tables, nil
}

// bucketTotals pairs each ledger bucket with its cached projection: the
// ledger sums signed lines per bucket, the projection sums the matching
// balance column. Both must agree for the migration to proceed.
func bucketTotals(ctx context.Context, pool *pgxpool.Pool) ([]bucketTotal, error) {
	ledger := map[string]bucketTotal{}
	rows, err := pool.Query(ctx,
		`SELECT bucket, count(*), COALESCE(SUM(amount), 0) FROM app.wallet_transactions GROUP BY bucket ORDER BY bucket`)
	if err != nil {
		return nil, fmt.Errorf("ledger buckets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var total bucketTotal
		var lines int64
		if err := rows.Scan(&total.Bucket, &lines, &total.LedgerSum); err != nil {
			return nil, fmt.Errorf("scan bucket: %w", err)
		}
		total.Operations = lines
		ledger[total.Bucket] = total
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate buckets: %w", err)
	}
	var freeSum, purchasedSum, freeHolders, purchasedHolders int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(balance_free), 0), COALESCE(SUM(balance_purchased), 0),
		       COUNT(DISTINCT CASE WHEN balance_free <> 0 THEN account_id END),
		       COUNT(DISTINCT CASE WHEN balance_purchased <> 0 THEN account_id END)
		FROM app.wallet_accounts`).Scan(&freeSum, &purchasedSum, &freeHolders, &purchasedHolders); err != nil {
		return nil, fmt.Errorf("projected buckets: %w", err)
	}
	projected := map[string][2]int64{
		"FREE_INK":      {freeSum, freeHolders},
		"PURCHASED_INK": {purchasedSum, purchasedHolders},
	}
	totals := []bucketTotal{}
	for _, bucket := range []string{"FREE_INK", "PURCHASED_INK"} {
		total := bucketTotal{Bucket: bucket}
		if entry, ok := ledger[bucket]; ok {
			total.Operations = entry.Operations
			total.LedgerSum = entry.LedgerSum
		}
		total.Projected = projected[bucket][0]
		total.Holders = projected[bucket][1]
		totals = append(totals, total)
	}
	return totals, nil
}

func operationTotals(ctx context.Context, pool *pgxpool.Pool) ([]operationTotal, error) {
	rows, err := pool.Query(ctx,
		`SELECT operation_type, count(*) FROM app.wallet_operations GROUP BY operation_type ORDER BY operation_type`)
	if err != nil {
		return nil, fmt.Errorf("operation totals: %w", err)
	}
	defer rows.Close()
	totals := []operationTotal{}
	for rows.Next() {
		var total operationTotal
		if err := rows.Scan(&total.Operation, &total.Count); err != nil {
			return nil, fmt.Errorf("scan operation: %w", err)
		}
		totals = append(totals, total)
	}
	return totals, rows.Err()
}

func passTotals(ctx context.Context, pool *pgxpool.Pool) ([]passTotal, error) {
	rows, err := pool.Query(ctx, `
		SELECT l.origin, count(*),
		       COALESCE(SUM(l.quantity), 0), COALESCE(SUM(l.remaining_quantity), 0),
		       COALESCE(SUM(consumed.n), 0),
		       COALESCE(SUM(CASE WHEN l.expires_at IS NOT NULL AND l.expires_at <= now() THEN 1 ELSE 0 END), 0),
		       COUNT(DISTINCT l.account_id)
		FROM app.arena_pass_lots l
		LEFT JOIN (SELECT lot_id, count(*) AS n FROM app.arena_pass_consumptions GROUP BY lot_id) consumed
		  ON consumed.lot_id = l.id
		GROUP BY l.origin
		ORDER BY l.origin`)
	if err != nil {
		return nil, fmt.Errorf("pass totals: %w", err)
	}
	defer rows.Close()
	totals := []passTotal{}
	for rows.Next() {
		var total passTotal
		if err := rows.Scan(&total.Origin, &total.Lots, &total.Quantity, &total.Remaining, &total.Consumed, &total.Expired, &total.Holders); err != nil {
			return nil, fmt.Errorf("scan pass: %w", err)
		}
		totals = append(totals, total)
	}
	return totals, rows.Err()
}

func subscriptionTotals(ctx context.Context, pool *pgxpool.Pool) ([]subscriptionTotal, error) {
	rows, err := pool.Query(ctx,
		`SELECT status, count(*) FROM app.subscriptions GROUP BY status ORDER BY status`)
	if err != nil {
		return nil, fmt.Errorf("subscription totals: %w", err)
	}
	defer rows.Close()
	totals := []subscriptionTotal{}
	for rows.Next() {
		var total subscriptionTotal
		if err := rows.Scan(&total.Status, &total.Count); err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		totals = append(totals, total)
	}
	return totals, rows.Err()
}

func orphanCounts(ctx context.Context, pool *pgxpool.Pool) (map[string]int64, error) {
	probes := map[string]string{
		"wallet_without_account":        `SELECT count(*) FROM app.wallet_accounts a LEFT JOIN app.accounts p ON p.id = a.account_id WHERE p.id IS NULL`,
		"operation_without_wallet":      `SELECT count(*) FROM app.wallet_operations o LEFT JOIN app.wallet_accounts a ON a.account_id = o.account_id WHERE a.account_id IS NULL`,
		"transaction_without_operation": `SELECT count(*) FROM app.wallet_transactions t LEFT JOIN app.wallet_operations o ON o.id = t.operation_id WHERE o.id IS NULL`,
		"lot_without_account":           `SELECT count(*) FROM app.arena_pass_lots l LEFT JOIN app.accounts p ON p.id = l.account_id WHERE p.id IS NULL`,
		"consumption_without_lot":       `SELECT count(*) FROM app.arena_pass_consumptions c LEFT JOIN app.arena_pass_lots l ON l.id = c.lot_id WHERE l.id IS NULL`,
		"subscription_without_customer": `SELECT count(*) FROM app.subscriptions s LEFT JOIN app.stripe_customers c ON c.account_id = s.account_id WHERE c.account_id IS NULL`,
		"customer_without_account":      `SELECT count(*) FROM app.stripe_customers c LEFT JOIN app.accounts p ON p.id = c.account_id WHERE p.id IS NULL`,
	}
	orphans := map[string]int64{}
	names := make([]string, 0, len(probes))
	for name := range probes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var count int64
		if err := pool.QueryRow(ctx, probes[name]).Scan(&count); err != nil {
			return nil, fmt.Errorf("orphan probe %s: %w", name, err)
		}
		orphans[name] = count
	}
	return orphans, nil
}

func shapeViolations(ctx context.Context, pool *pgxpool.Pool) (map[string]int64, int64, int64, int64, error) {
	unknowns := map[string]int64{}
	probes := map[string]string{
		"unknown_bucket":    `SELECT count(*) FROM app.wallet_transactions WHERE bucket NOT IN ('FREE_INK', 'PURCHASED_INK')`,
		"unknown_operation": `SELECT count(*) FROM app.wallet_operations WHERE operation_type NOT IN ('credit_free', 'credit_member', 'credit_purchase', 'credit_refund', 'credit_admin', 'debit_argument', 'debit_admin', 'expire_free')`,
		"unknown_origin":    `SELECT count(*) FROM app.arena_pass_lots WHERE origin NOT IN ('PURCHASE', 'MEMBER', 'ADMIN')`,
		"unknown_status":    `SELECT count(*) FROM app.subscriptions WHERE status NOT IN ('incomplete', 'incomplete_expired', 'trialing', 'active', 'past_due', 'canceled', 'unpaid', 'paused')`,
	}
	names := make([]string, 0, len(probes))
	for name := range probes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var count int64
		if err := pool.QueryRow(ctx, probes[name]).Scan(&count); err != nil {
			return nil, 0, 0, 0, fmt.Errorf("unknown probe %s: %w", name, err)
		}
		unknowns[name] = count
	}
	var negatives, zero, over int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM app.wallet_accounts WHERE balance_free < 0 OR balance_purchased < 0`).Scan(&negatives); err != nil {
		return nil, 0, 0, 0, fmt.Errorf("negative probe: %w", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM app.wallet_transactions WHERE amount = 0`).Scan(&zero); err != nil {
		return nil, 0, 0, 0, fmt.Errorf("zero probe: %w", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM app.arena_pass_lots WHERE remaining_quantity > quantity OR remaining_quantity < 0`).Scan(&over); err != nil {
		return nil, 0, 0, 0, fmt.Errorf("over-consumption probe: %w", err)
	}
	return unknowns, negatives, zero, over, nil
}

// classify judges the snapshot against the closed vocabularies and the
// conservation rules. Every unknown value and every orphan blocks the
// migration the report would otherwise allow.
func classify(snap snapshot) []Finding {
	findings := []Finding{}
	for name, count := range snap.Orphans {
		if count > 0 {
			findings = append(findings, Finding{Rule: "orphan-" + strings.TrimPrefix(name, "orphan_"), Detail: orphansDetail(name, count)})
		}
	}
	for name, count := range snap.Unknowns {
		if count > 0 {
			findings = append(findings, Finding{Rule: name, Detail: unknownsDetail(name, count)})
		}
	}
	if snap.NegativeWallets > 0 {
		findings = append(findings, Finding{Rule: "negative-balance", Detail: negativeDetail(snap.NegativeWallets)})
	}
	if snap.ZeroAmounts > 0 {
		findings = append(findings, Finding{Rule: "zero-amount", Detail: zeroDetail(snap.ZeroAmounts)})
	}
	if snap.OverConsumed > 0 {
		findings = append(findings, Finding{Rule: "over-consumed", Detail: overDetail(snap.OverConsumed)})
	}
	for _, bucket := range snap.Buckets {
		if bucket.LedgerSum != bucket.Projected {
			findings = append(findings, Finding{Rule: "ledger-projection-mismatch", Detail: ledgerDetail(bucket)})
		}
	}
	for _, pass := range snap.Passes {
		if pass.Remaining+pass.Consumed != pass.Quantity {
			findings = append(findings, Finding{Rule: "lot-arithmetic-mismatch", Detail: lotDetail(pass)})
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Rule != findings[j].Rule {
			return findings[i].Rule < findings[j].Rule
		}
		return findings[i].Detail < findings[j].Detail
	})
	return findings
}

func orphansDetail(name string, count int64) string {
	return strings.ReplaceAll(name, "_", " ") + ": rows without a parent"
}

func unknownsDetail(name string, count int64) string {
	return strings.ReplaceAll(name, "_", " ") + ": values outside the closed vocabulary"
}

func negativeDetail(count int64) string {
	return "wallets with a negative bucket balance"
}

func zeroDetail(count int64) string {
	return "ledger lines with zero amount"
}

func overDetail(count int64) string {
	return "pass lots consumed beyond quantity"
}

func ledgerDetail(bucket bucketTotal) string {
	return "bucket " + bucket.Bucket + ": ledger and projection disagree"
}

func lotDetail(pass passTotal) string {
	return "origin " + pass.Origin + ": remaining plus consumed differs from quantity"
}

// summarize renders the classified contracts: franchise and purchased
// INK by bucket, passes by origin, subscriptions by provider status.
func summarize(snap snapshot) []ContractSummary {
	contracts := []ContractSummary{}
	for _, bucket := range snap.Buckets {
		contract, origin, term, obligation := "purchased-ink", "stripe", "open-ended", "refundable-unused"
		if bucket.Bucket == "FREE_INK" {
			contract, origin, term, obligation = "franchise", "plan", "period", "non-refundable"
		}
		contracts = append(contracts, ContractSummary{
			Contract: contract, Origin: origin, Term: term, Obligation: obligation,
			Rows: bucket.Operations, Total: bucket.LedgerSum, Accounts: bucket.Holders,
		})
	}
	for _, pass := range snap.Passes {
		term := "open-ended"
		if pass.Origin == "MEMBER" {
			term = "period"
		}
		contracts = append(contracts, ContractSummary{
			Contract: "arena-pass", Origin: strings.ToLower(pass.Origin), Term: term, Obligation: "honor-remaining",
			Rows: pass.Lots, Total: pass.Remaining, Accounts: pass.Holders,
		})
	}
	for _, subscription := range snap.Subscriptions {
		contracts = append(contracts, ContractSummary{
			Contract: "member-subscription", Origin: "stripe", Term: "period", Obligation: "honor-remaining",
			Rows: subscription.Count, Total: subscription.Count, Accounts: subscription.Count,
		})
	}
	sort.Slice(contracts, func(i, j int) bool {
		if contracts[i].Contract != contracts[j].Contract {
			return contracts[i].Contract < contracts[j].Contract
		}
		return contracts[i].Origin < contracts[j].Origin
	})
	return contracts
}

// Audit snapshots one database and classifies it. Empty findings mean
// the books reconcile and the migration may proceed to planning.
func Audit(ctx context.Context, pool *pgxpool.Pool) (Report, error) {
	snap, err := snapshotDatabase(ctx, pool)
	if err != nil {
		return Report{}, err
	}
	findings := classify(snap)
	if findings == nil {
		findings = []Finding{}
	}
	contracts := summarize(snap)
	if contracts == nil {
		contracts = []ContractSummary{}
	}
	return Report{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Tables:      snap.Tables,
		Contracts:   contracts,
		Findings:    findings,
	}, nil
}

// Render encodes the report. The encoding carries aggregates only;
// account identifiers and emails never enter it by construction.
func Render(report Report) ([]byte, error) {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode report: %w", err)
	}
	return append(encoded, '\n'), nil
}
