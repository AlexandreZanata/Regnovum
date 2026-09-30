// The financial half of the disaster drill (P20-T05).
//
// The phase asks that a backup be restored in an isolated environment, that the
// application come up on the restored data, and that the *financial integrity*
// be checked. "Checked" cannot mean a row count: an INK ledger that lost a
// transaction, or whose cached projection drifted from the ledger it derives
// from, still has a row count. So this file reads the ledger twice — once
// before the loss and once after the recovery — and compares the two readings,
// plus the invariants the schema declares, as values.
//
// Everything here is a pure function over what was read: the queries fill the
// structure and the rules judge it, which is what lets the tests break one row at
// a time and prove that each rule is the one that refuses.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Wallet is one account's INK position: what the projection says and what the
// append-only ledger derives to, which have to be the same number.
type Wallet struct {
	AccountID        string `json:"account_id"`
	StoredFree       int64  `json:"stored_free"`
	StoredPurchased  int64  `json:"stored_purchased"`
	DerivedFree      int64  `json:"derived_free"`
	DerivedPurchased int64  `json:"derived_purchased"`
	Rows             int64  `json:"rows"`
	Operations       int64  `json:"operations"`
}

// Ledger is one reading of a cluster's financial state.
type Ledger struct {
	// CapturedAt is when the reading was taken, in the cluster's own clock.
	CapturedAt string `json:"captured_at"`
	// Target is what was asked for: the DSN's host and database, never a
	// credential.
	Target string `json:"target"`
	// Server is the version the cluster reported.
	Server string `json:"server"`
	// ArchiveTimeoutSeconds is the archive_timeout the cluster runs with, read
	// from the server rather than from a file: it is the bound the observed RPO
	// is judged against, and a copy of it in a document would be a second
	// opinion about the same setting.
	ArchiveTimeoutSeconds int64 `json:"archive_timeout_seconds"`
	// Accounts is how many accounts the cluster holds.
	Accounts int64 `json:"accounts"`
	// Wallets is one entry per account that has a wallet.
	Wallets []Wallet `json:"wallets"`
	// Operations and Transactions size the ledger.
	Operations   int64 `json:"operations"`
	Transactions int64 `json:"transactions"`
	// LedgerDigest identifies the ledger rows themselves, ordered by id, so a
	// lost or edited row changes it.
	LedgerDigest string `json:"ledger_digest"`
	// BalancesDigest identifies the projection.
	BalancesDigest string `json:"balances_digest"`
	// FreeINK and PurchasedINK are the totals of the ledger: money is neither
	// created nor destroyed by a recovery.
	FreeINK      int64 `json:"free_ink"`
	PurchasedINK int64 `json:"purchased_ink"`
	// Jobs is the queue the drill also writes to, so the direction of the
	// point-in-time boundary is observable on rows the restore did and did not
	// have to bring back.
	Jobs int64 `json:"jobs"`
}

// Reading is the ledger plus the raw archive figures the report needs.
type Reading struct {
	Ledger Ledger `json:"ledger"`
	// ArchiveFailed is pg_stat_archiver.failed_count: a cluster whose archive
	// is failing is a cluster whose backup is behind, and the drill refuses to
	// call that a recovery point.
	ArchiveFailed int64 `json:"archive_failed"`
	// ArchiveLagSeconds is how stale the archive was when the reading was
	// taken, or -1 when the cluster has never archived.
	ArchiveLagSeconds int64 `json:"archive_lag_seconds"`
}

// Violation is one refused rule, addressed to the account or the area it is
// about.
type Violation struct {
	// Rule is the name of the rule, so a test can assert which one fired.
	Rule string `json:"rule"`
	// Subject is the account, the table or the figure the violation is about.
	Subject string `json:"subject"`
	// Detail states what was read and what was expected.
	Detail string `json:"detail"`
}

// String renders a violation the way the other auditors do.
func (v Violation) String() string {
	if v.Subject == "" {
		return fmt.Sprintf("%s: %s", v.Rule, v.Detail)
	}
	return fmt.Sprintf("%s: %s: %s", v.Rule, v.Subject, v.Detail)
}

// ReadLedger reads the financial state of one cluster.
func ReadLedger(ctx context.Context, db *sql.DB, target string) (Reading, error) {
	var reading Reading
	ledger := &reading.Ledger
	ledger.Target = target

	if err := db.QueryRowContext(ctx, `SELECT current_setting('server_version'), to_char(now(), 'YYYY-MM-DD HH24:MI:SS.US')`).
		Scan(&ledger.Server, &ledger.CapturedAt); err != nil {
		return reading, fmt.Errorf("read the cluster identity: %w", err)
	}

	// The interval cast is what makes the reading independent of the unit the
	// server prints: `5min` and `300` both become 300 seconds.
	if err := db.QueryRowContext(ctx, `SELECT extract(epoch FROM current_setting('archive_timeout')::interval)::bigint`).
		Scan(&ledger.ArchiveTimeoutSeconds); err != nil {
		return reading, fmt.Errorf("read archive_timeout: %w", err)
	}

	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM app.accounts`).Scan(&ledger.Accounts); err != nil {
		return reading, fmt.Errorf("count the accounts: %w", err)
	}

	// The projection and the ledger it derives from, read in one statement so
	// the two numbers come from the same instant. A wallet row that exists
	// without any ledger row counts as a derivation of zero, which is how a
	// projection that was written but never posted shows up as a mismatch
	// instead of as a missing account.
	rows, err := db.QueryContext(ctx, `
		SELECT w.account_id::text,
		       w.balance_free, w.balance_purchased,
		       COALESCE(d.free_sum, 0), COALESCE(d.purchased_sum, 0),
		       COALESCE(d.rows, 0), COALESCE(d.operations, 0)
		FROM app.wallet_accounts w
		LEFT JOIN (
			SELECT o.account_id,
			       sum(t.amount) FILTER (WHERE t.bucket = 'FREE_INK') AS free_sum,
			       sum(t.amount) FILTER (WHERE t.bucket = 'PURCHASED_INK') AS purchased_sum,
			       count(*) AS rows,
			       count(DISTINCT t.operation_id) AS operations
			FROM app.wallet_transactions t
			JOIN app.wallet_operations o ON o.id = t.operation_id
			GROUP BY o.account_id
		) d ON d.account_id = w.account_id
		ORDER BY w.account_id`)
	if err != nil {
		return reading, fmt.Errorf("read the wallets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var wallet Wallet
		if err := rows.Scan(&wallet.AccountID, &wallet.StoredFree, &wallet.StoredPurchased,
			&wallet.DerivedFree, &wallet.DerivedPurchased, &wallet.Rows, &wallet.Operations); err != nil {
			return reading, fmt.Errorf("read a wallet: %w", err)
		}
		ledger.Wallets = append(ledger.Wallets, wallet)
	}
	if err := rows.Err(); err != nil {
		return reading, fmt.Errorf("read the wallets: %w", err)
	}

	if err := db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM app.wallet_operations),
		       (SELECT count(*) FROM app.wallet_transactions),
		       (SELECT COALESCE(md5(string_agg(id::text || ':' || operation_id::text || ':' || bucket || ':' || amount::text, '|' ORDER BY id)), 'none') FROM app.wallet_transactions),
		       (SELECT COALESCE(md5(string_agg(account_id::text || ':' || balance_free::text || ':' || balance_purchased::text, '|' ORDER BY account_id)), 'none') FROM app.wallet_accounts),
		       (SELECT COALESCE(sum(amount) FILTER (WHERE bucket = 'FREE_INK'), 0) FROM app.wallet_transactions),
		       (SELECT COALESCE(sum(amount) FILTER (WHERE bucket = 'PURCHASED_INK'), 0) FROM app.wallet_transactions),
		       (SELECT count(*) FROM app.jobs)
	`).Scan(&ledger.Operations, &ledger.Transactions, &ledger.LedgerDigest, &ledger.BalancesDigest,
		&ledger.FreeINK, &ledger.PurchasedINK, &ledger.Jobs); err != nil {
		return reading, fmt.Errorf("read the ledger totals: %w", err)
	}

	if err := db.QueryRowContext(ctx, `
		SELECT failed_count,
		       COALESCE(floor(extract(epoch FROM (now() - last_archived_time)))::bigint, -1)
		FROM pg_stat_archiver`).Scan(&reading.ArchiveFailed, &reading.ArchiveLagSeconds); err != nil {
		return reading, fmt.Errorf("read the archiver: %w", err)
	}
	return reading, nil
}

// Violations applies the invariants the schema declares to one reading. They are
// invariants and not preferences: the migration states that a balance is never
// negative, that the ledger is the source of truth the projection derives from,
// and that one operation touches each bucket at most once.
func (l Ledger) Violations() []Violation {
	var violations []Violation
	refuse := func(rule, subject, detail string) {
		violations = append(violations, Violation{Rule: rule, Subject: subject, Detail: detail})
	}

	var derivedFree, derivedPurchased int64
	for _, wallet := range l.Wallets {
		derivedFree += wallet.DerivedFree
		derivedPurchased += wallet.DerivedPurchased
		if wallet.StoredFree < 0 || wallet.StoredPurchased < 0 {
			refuse("negative-balance", wallet.AccountID, fmt.Sprintf(
				"the projection holds FREE_INK=%d and PURCHASED_INK=%d; the schema forbids a negative balance",
				wallet.StoredFree, wallet.StoredPurchased))
		}
		if wallet.StoredFree != wallet.DerivedFree || wallet.StoredPurchased != wallet.DerivedPurchased {
			refuse("derived-balance", wallet.AccountID, fmt.Sprintf(
				"the projection says FREE_INK=%d/PURCHASED_INK=%d and the ledger derives FREE_INK=%d/PURCHASED_INK=%d",
				wallet.StoredFree, wallet.StoredPurchased, wallet.DerivedFree, wallet.DerivedPurchased))
		}
		if wallet.StoredFree != 0 || wallet.StoredPurchased != 0 {
			if wallet.Rows == 0 {
				refuse("ledger-missing", wallet.AccountID, "the projection holds INK and the ledger has no row that gives it")
			}
		}
		if wallet.Rows < wallet.Operations {
			refuse("orphan-operation", wallet.AccountID, fmt.Sprintf(
				"the account has %d ledger row(s) for %d operation(s): an operation touches at least one bucket",
				wallet.Rows, wallet.Operations))
		}
		if wallet.Operations > 0 && wallet.Rows > 2*wallet.Operations {
			refuse("duplicate-bucket", wallet.AccountID, fmt.Sprintf(
				"the account has %d ledger row(s) for %d operation(s): one operation touches each bucket at most once",
				wallet.Rows, wallet.Operations))
		}
	}

	if derivedFree != l.FreeINK || derivedPurchased != l.PurchasedINK {
		refuse("aggregate-total", "wallet_transactions", fmt.Sprintf(
			"the totals say FREE_INK=%d/PURCHASED_INK=%d and the wallets add up to FREE_INK=%d/PURCHASED_INK=%d",
			l.FreeINK, l.PurchasedINK, derivedFree, derivedPurchased))
	}
	if l.Transactions < l.Operations {
		refuse("ledger-smaller-than-operations", "wallet_transactions", fmt.Sprintf(
			"%d transaction(s) for %d operation(s)", l.Transactions, l.Operations))
	}
	if l.Transactions > 2*l.Operations {
		refuse("ledger-larger-than-operations", "wallet_transactions", fmt.Sprintf(
			"%d transaction(s) for %d operation(s): two buckets per operation is the ceiling", l.Transactions, l.Operations))
	}
	return violations
}

// Compare judges what came back against what was there. It is the heart of the
// financial assertion: the recovery has to bring back the same money, the same
// rows and the same projection, and it has to bring back nothing that was not
// there — a restore that *adds* a wallet is a restore nobody can trust either.
func Compare(baseline, restored Ledger) []Violation {
	var violations []Violation
	refuse := func(rule, subject, detail string) {
		violations = append(violations, Violation{Rule: rule, Subject: subject, Detail: detail})
	}

	before := map[string]Wallet{}
	for _, wallet := range baseline.Wallets {
		before[wallet.AccountID] = wallet
	}
	for _, wallet := range restored.Wallets {
		was, known := before[wallet.AccountID]
		if !known {
			refuse("wallet-added", wallet.AccountID, "the restored cluster holds a wallet that the baseline did not")
			continue
		}
		if was.StoredFree != wallet.StoredFree || was.StoredPurchased != wallet.StoredPurchased ||
			was.DerivedFree != wallet.DerivedFree || was.DerivedPurchased != wallet.DerivedPurchased {
			refuse("wallet-mismatch", wallet.AccountID, fmt.Sprintf(
				"the baseline held FREE_INK=%d/PURCHASED_INK=%d (derived %d/%d) and the restored cluster holds %d/%d (derived %d/%d)",
				was.StoredFree, was.StoredPurchased, was.DerivedFree, was.DerivedPurchased,
				wallet.StoredFree, wallet.StoredPurchased, wallet.DerivedFree, wallet.DerivedPurchased))
		}
	}
	seen := map[string]bool{}
	for _, wallet := range restored.Wallets {
		seen[wallet.AccountID] = true
	}
	for _, wallet := range baseline.Wallets {
		if !seen[wallet.AccountID] {
			refuse("wallet-lost", wallet.AccountID, "the baseline held this wallet and the restored cluster does not")
		}
	}

	if baseline.LedgerDigest != restored.LedgerDigest {
		refuse("ledger-digest", "wallet_transactions", fmt.Sprintf(
			"the ledger hashes to %s and the baseline to %s: a row was lost, added or edited",
			orNone(restored.LedgerDigest), orNone(baseline.LedgerDigest)))
	}
	if baseline.BalancesDigest != restored.BalancesDigest {
		refuse("balances-digest", "wallet_accounts", fmt.Sprintf(
			"the projection hashes to %s and the baseline to %s",
			orNone(restored.BalancesDigest), orNone(baseline.BalancesDigest)))
	}
	if baseline.FreeINK != restored.FreeINK || baseline.PurchasedINK != restored.PurchasedINK {
		refuse("total-mismatch", "wallet_transactions", fmt.Sprintf(
			"the baseline totalled FREE_INK=%d/PURCHASED_INK=%d and the restored cluster totals %d/%d",
			baseline.FreeINK, baseline.PurchasedINK, restored.FreeINK, restored.PurchasedINK))
	}
	if baseline.Accounts != restored.Accounts {
		refuse("accounts-mismatch", "accounts", fmt.Sprintf(
			"the baseline held %d account(s) and the restored cluster holds %d", baseline.Accounts, restored.Accounts))
	}
	if baseline.Operations != restored.Operations || baseline.Transactions != restored.Transactions {
		refuse("ledger-size", "wallet_transactions", fmt.Sprintf(
			"the baseline held %d operation(s)/%d transaction(s) and the restored cluster holds %d/%d",
			baseline.Operations, baseline.Transactions, restored.Operations, restored.Transactions))
	}
	if baseline.Jobs != restored.Jobs {
		refuse("jobs-mismatch", "jobs", fmt.Sprintf(
			"the baseline held %d job(s) and the restored cluster holds %d: the drill writes no job between the two, so a difference is the boundary being in the wrong place",
			baseline.Jobs, restored.Jobs))
	}
	return violations
}

// orNone keeps an empty digest readable in a message.
func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(nothing)"
	}
	return value
}
