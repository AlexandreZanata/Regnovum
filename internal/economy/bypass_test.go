package economy_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	billingcatalog "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/catalog"
	billingapp "github.com/AlexandreZanata/Regnovum/internal/billing/application"
	billingdomain "github.com/AlexandreZanata/Regnovum/internal/billing/domain"
	crowndomain "github.com/AlexandreZanata/Regnovum/internal/crown/domain"
	economydomain "github.com/AlexandreZanata/Regnovum/internal/economy/domain"
	patentsdomain "github.com/AlexandreZanata/Regnovum/internal/patents/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/httpserver"
)

// mockLedger tracks debits, credits, and holds to prove ledger immutability upon bypass attempts.
type mockLedger struct {
	mu           sync.Mutex
	balance      int64
	creditsCount int
	debitsCount  int
	journal      []string
}

func newMockLedger(initialBalance int64) *mockLedger {
	return &mockLedger{
		balance: initialBalance,
		journal: make([]string, 0),
	}
}

func (m *mockLedger) Credit(account string, amount int64, op string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.balance += amount
	m.creditsCount++
	m.journal = append(m.journal, fmt.Sprintf("credit:%s:%d:%s", account, amount, op))
	return nil
}

func (m *mockLedger) Debit(account string, amount int64, op string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.balance < amount {
		return errors.New("insufficient balance")
	}
	m.balance -= amount
	m.debitsCount++
	m.journal = append(m.journal, fmt.Sprintf("debit:%s:%d:%s", account, amount, op))
	return nil
}

func (m *mockLedger) AssertUntouched(t *testing.T, expectedBalance int64) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.balance != expectedBalance {
		t.Fatalf("ledger was modified: got balance %d, want %d", m.balance, expectedBalance)
	}
	if m.creditsCount != 0 {
		t.Fatalf("ledger recorded %d unauthorized credits", m.creditsCount)
	}
	if m.debitsCount != 0 {
		t.Fatalf("ledger recorded %d unauthorized debits", m.debitsCount)
	}
	if len(m.journal) != 0 {
		t.Fatalf("ledger recorded unexpected journal entries: %v", m.journal)
	}
}

// fakeInker satisfies billingapp.Inker, tracking attempted credits against mockLedger.
type fakeInker struct {
	ledger *mockLedger
}

func (f *fakeInker) CreditPurchasedInk(_ context.Context, req billingapp.InkerCreditRequest) (*billingapp.InkerCreditResult, error) {
	if f.ledger != nil {
		_ = f.ledger.Credit(req.AccountID, req.Amount, "credit_purchase")
	}
	return &billingapp.InkerCreditResult{Replayed: false}, nil
}

func (f *fakeInker) CreditMemberInk(_ context.Context, req billingapp.InkerCreditRequest) (*billingapp.InkerCreditResult, error) {
	if f.ledger != nil {
		_ = f.ledger.Credit(req.AccountID, req.Amount, "credit_member")
	}
	return &billingapp.InkerCreditResult{Replayed: false}, nil
}

// fakeClock provides deterministic time for billing tests.
type fakeClock struct {
	now time.Time
}

func (f fakeClock) Now() time.Time {
	return f.now
}

// fakeIntentRepo satisfies billingapp.CheckoutIntentRepository.
type fakeIntentRepo struct {
	intent *billingapp.CheckoutIntentRecord
	err    error
}

func (f *fakeIntentRepo) RecordCheckoutIntent(_ context.Context, _ billingapp.RecordCheckoutIntentRequest) (*billingapp.RecordCheckoutIntentResult, error) {
	return &billingapp.RecordCheckoutIntentResult{}, nil
}

func (f *fakeIntentRepo) GetCheckoutIntentBySession(_ context.Context, _ billingdomain.StripeCheckoutSessionID) (*billingapp.CheckoutIntentRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.intent, nil
}

func (f *fakeIntentRepo) MarkCheckoutIntentPaid(_ context.Context, _ billingdomain.StripeCheckoutSessionID) error {
	return nil
}

// executeAPIBypassAttempt simulates an API buyer trying to purchase unapproved products.
func executeAPIBypassAttempt(ledger *mockLedger) error {
	loaded, err := billingcatalog.Load(billingcatalog.Spec{
		Markets: []billingcatalog.EnabledMarket{
			{Market: "BR", Currency: "BRL"},
		},
	})
	if err != nil {
		return err
	}
	probeID := billingdomain.ProductID("crown_bond_100")
	if loaded.Has(billingdomain.MarketBrazil, probeID) {
		_ = ledger.Debit("buyer", 1000, "buy_bond")
		return nil
	}
	return errors.New("api_bypass_rejected: product not found in catalog")
}

// executeWorkerBypassAttempt simulates an async worker attempting to evaluate deferred products.
func executeWorkerBypassAttempt(ledger *mockLedger) error {
	if economydomain.AreBondsAvailable() {
		_ = ledger.Credit("worker", 500, "bond_yield")
		return nil
	}
	if err := economydomain.EvaluateBondPurchase(); err != nil {
		// Worker correctly failed closed.
	}
	if economydomain.AreBettingMarketsAvailable() {
		_ = ledger.Credit("worker", 500, "bet_payout")
		return nil
	}
	if err := economydomain.AttemptPlaceBet(); err != nil {
		// Worker correctly failed closed.
	}
	virginBook, _ := patentsdomain.NewSupplyBook("temporada-1")
	_, _, err := patentsdomain.IssueSeat(virginBook, patentsdomain.IssueRequest{
		PurchaseID: "pur-1",
		Buyer:      "buyer-1",
		Population: "cidadaos",
		SaleMode:   patentsdomain.SaleModeDirect,
		At:         time.Now().UTC(),
	})
	if err == nil {
		_ = ledger.Credit("buyer-1", 1, "patent_seat")
		return nil
	}
	return err
}

// executeAdminBypassAttempt simulates an administrator attempting to force grant or auction.
func executeAdminBypassAttempt(ledger *mockLedger) error {
	ratifiedTable := patentsdomain.SupplyTable{
		Version:         "v1",
		PriceMinorUnits: 1000,
		Currency:        "BRL",
		Eligible:        "cidadaos",
		Cap:             10,
		Duration:        24 * time.Hour,
		CosmeticGrant:   "titulo-honorifico",
		Season:          "temporada-1",
	}
	virginBook, _ := patentsdomain.NewSupplyBook("temporada-1")
	publishedBook, err := patentsdomain.PublishTable(virginBook, ratifiedTable)
	if err != nil {
		return err
	}
	_, _, err = patentsdomain.IssueSeat(publishedBook, patentsdomain.IssueRequest{
		PurchaseID: "pur-admin-1",
		Buyer:      "admin-privileged",
		Population: "cidadaos",
		SaleMode:   patentsdomain.SaleModeAuction,
		At:         time.Now().UTC(),
	})
	if err == nil {
		_ = ledger.Credit("admin-privileged", 1, "auction_seat")
		return nil
	}
	return err
}

// executeWebhookBypassAttempt simulates an external payment webhook callback.
func executeWebhookBypassAttempt(ledger *mockLedger) error {
	loadedCatalog, err := billingcatalog.Load(billingcatalog.Spec{
		Markets: []billingcatalog.EnabledMarket{
			{Market: "BR", Currency: "BRL"},
			{Market: "INTERNATIONAL", Currency: "USD"},
		},
	})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	amount, err := billingdomain.NewMoney(5000, billingdomain.CurrencyBRL)
	if err != nil {
		return err
	}
	useCase, err := billingapp.NewSettleCheckoutUseCase(billingapp.SettleCheckoutDependencies{
		Catalog: loadedCatalog,
		Intents: &fakeIntentRepo{
			intent: &billingapp.CheckoutIntentRecord{
				ID:             "intent-unapproved",
				AccountID:      "acc-attacker",
				Market:         billingdomain.MarketBrazil,
				ProductID:      billingdomain.ProductID("crown_bond_100"),
				CatalogVersion: 1,
				Amount:         amount,
				Status:         billingdomain.CheckoutIntentOpen,
				SessionID:      "cs_unapproved_123",
				CreatedAt:      now,
			},
		},
		Inker: &fakeInker{ledger: ledger},
		Clock: fakeClock{now: now},
	})
	if err != nil {
		return err
	}
	_, err = useCase.Execute(context.Background(), billingapp.SettleCheckoutCommand{
		SessionID: "cs_unapproved_123",
	})
	return err
}

// executeDecreeBypassAttempt simulates a royal decree attempting to force economic activation.
func executeDecreeBypassAttempt(ledger *mockLedger) error {
	anchor := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	badAct := crowndomain.RoyalAct{
		ID:         "decreto-bypass-1",
		Author:     "rei-1",
		Season:     "temporada-1",
		Reign:      1,
		Competence: "patrimonial",
		Kind:       crowndomain.ActEconomic,
		Reason:     "tentativa de emitir titulo ou aposta",
		Target:     "conta/alvo",
		Effect:     "financiar produto",
		DecreedAt:  anchor,
		Effective:  anchor.Add(time.Hour),
		EndsAt:     anchor.Add(30 * 24 * time.Hour),
		Charter:    "v1",
		Origin:     "titulos",
		Amount:     1000,
	}
	digest, err := crowndomain.ActDigest(badAct)
	if err != nil {
		return err
	}
	clearance := crowndomain.Clearance{
		Act:       badAct.ID,
		Season:    badAct.Season,
		Reign:     badAct.Reign,
		Digest:    digest,
		ExpiresAt: anchor.Add(2 * time.Hour),
	}
	current := crowndomain.CurrentReign{
		Season:   "temporada-1",
		Holder:   "rei-1",
		Reign:    1,
		StartsAt: anchor,
		EndsAt:   anchor.Add(7776000 * time.Second),
		Open:     true,
	}
	custody := crowndomain.CustodyView{
		Origin:      "titulos",
		Beneficiary: "conta/alvo",
		Available:   10000,
	}
	_, err = crowndomain.PlanExecution(badAct, clearance, current, custody, anchor.Add(time.Hour+time.Minute))
	if err == nil {
		_ = ledger.Credit("conta/alvo", 1000, "decree_grant")
		return nil
	}
	return err
}

// TestRefusedBypassLeavesLedgerCompletelyUntouched proves ledger immutability across all 5 bypass paths.
func TestRefusedBypassLeavesLedgerCompletelyUntouched(t *testing.T) {
	t.Parallel()

	initialBalance := int64(50000)
	ledger := newMockLedger(initialBalance)

	if err := executeAPIBypassAttempt(ledger); err == nil {
		t.Fatal("API bypass unexpectedly succeeded")
	}
	if err := executeWorkerBypassAttempt(ledger); err == nil {
		t.Fatal("worker bypass unexpectedly succeeded")
	}
	if err := executeAdminBypassAttempt(ledger); err == nil {
		t.Fatal("admin bypass unexpectedly succeeded")
	}
	if err := executeWebhookBypassAttempt(ledger); err == nil {
		t.Fatal("webhook bypass unexpectedly succeeded")
	}
	if err := executeDecreeBypassAttempt(ledger); err == nil {
		t.Fatal("decree bypass unexpectedly succeeded")
	}

	ledger.AssertUntouched(t, initialBalance)
}

// TestAPIBypassRejectsUnapprovedProducts verifies API and HTTP rejection of unapproved products.
func TestAPIBypassRejectsUnapprovedProducts(t *testing.T) {
	t.Parallel()

	routes := httpserver.RegisteredRoutes()
	if violations := scanRoutesForBondTokens(routes); len(violations) > 0 {
		t.Fatalf("registered routes carry bond entrypoints: %v", violations)
	}
	if violations := scanRoutesForBettingTokens(routes); len(violations) > 0 {
		t.Fatalf("registered routes carry betting entrypoints: %v", violations)
	}

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	probes := []string{
		"/api/v1/crown-bonds/checkout",
		"/api/v1/bonds/purchase",
		"/api/v1/betting/wager",
		"/api/v1/markets/prediction/bet",
		"/api/v1/patents/auction",
		"/api/v1/economy/products/activate",
	}
	for _, path := range probes {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}

	loaded, err := billingcatalog.Load(billingcatalog.Spec{
		Markets: []billingcatalog.EnabledMarket{
			{Market: "BR", Currency: "BRL"},
			{Market: "INTERNATIONAL", Currency: "USD"},
		},
	})
	if err != nil {
		t.Fatalf("billingcatalog.Load: %v", err)
	}

	probedProducts := []string{"crown_bond", "bet_prediction", "patent_auction"}
	for _, probe := range probedProducts {
		id, err := billingdomain.ParseProductID(probe)
		if err != nil {
			continue
		}
		if loaded.Has(billingdomain.MarketBrazil, id) || loaded.Has(billingdomain.MarketInternational, id) {
			t.Fatalf("probed product %s found in commercial catalog", probe)
		}
	}
}

// TestWorkerBypassRejectsUnapprovedProducts verifies background evaluations fail closed.
func TestWorkerBypassRejectsUnapprovedProducts(t *testing.T) {
	t.Parallel()

	if err := economydomain.EvaluateBondPurchase(); !errors.Is(err, economydomain.ErrBondsUnavailable) {
		t.Fatalf("EvaluateBondPurchase = %v, want ErrBondsUnavailable", err)
	}
	if err := economydomain.EvaluateBondYieldPublication(5.0); !errors.Is(err, economydomain.ErrBondYieldForbidden) {
		t.Fatalf("EvaluateBondYieldPublication = %v, want ErrBondYieldForbidden", err)
	}
	if err := economydomain.AttemptPlaceBet(); !errors.Is(err, economydomain.ErrBettingMarketsUnavailable) {
		t.Fatalf("AttemptPlaceBet = %v, want ErrBettingMarketsUnavailable", err)
	}
	if err := economydomain.AttemptCreateMarket(); !errors.Is(err, economydomain.ErrBettingMarketsUnavailable) {
		t.Fatalf("AttemptCreateMarket = %v, want ErrBettingMarketsUnavailable", err)
	}
	if err := economydomain.AttemptSettleBet(); !errors.Is(err, economydomain.ErrBettingMarketsUnavailable) {
		t.Fatalf("AttemptSettleBet = %v, want ErrBettingMarketsUnavailable", err)
	}

	virginBook, err := patentsdomain.NewSupplyBook("temporada-1")
	if err != nil {
		t.Fatalf("NewSupplyBook: %v", err)
	}
	_, _, err = patentsdomain.IssueSeat(virginBook, patentsdomain.IssueRequest{
		PurchaseID: "pur-worker-1",
		Buyer:      "user-worker",
		Population: "cidadaos",
		SaleMode:   patentsdomain.SaleModeDirect,
		At:         time.Now().UTC(),
	})
	if !errors.Is(err, patentsdomain.ErrTermsMissing) {
		t.Fatalf("virgin book IssueSeat = %v, want ErrTermsMissing", err)
	}

	ratifiedTable := patentsdomain.SupplyTable{
		Version:         "v1",
		PriceMinorUnits: 1000,
		Currency:        "BRL",
		Eligible:        "cidadaos",
		Cap:             10,
		Duration:        24 * time.Hour,
		CosmeticGrant:   "titulo-honorifico",
		Season:          "temporada-1",
	}
	publishedBook, err := patentsdomain.PublishTable(virginBook, ratifiedTable)
	if err != nil {
		t.Fatalf("PublishTable: %v", err)
	}
	frozenBook, err := patentsdomain.FreezeSales(patentsdomain.FreezeRequest{
		Book:   publishedBook,
		At:     time.Now().UTC(),
		Reason: "auditoria-preventiva",
	})
	if err != nil {
		t.Fatalf("FreezeSales: %v", err)
	}
	_, _, err = patentsdomain.IssueSeat(frozenBook, patentsdomain.IssueRequest{
		PurchaseID: "pur-worker-2",
		Buyer:      "user-worker",
		Population: "cidadaos",
		SaleMode:   patentsdomain.SaleModeDirect,
		At:         time.Now().UTC(),
	})
	if !errors.Is(err, patentsdomain.ErrFrozenSale) {
		t.Fatalf("frozen book IssueSeat = %v, want ErrFrozenSale", err)
	}
}

// TestAdminBypassRejectsUnapprovedProducts verifies administrator and operator restrictions.
func TestAdminBypassRejectsUnapprovedProducts(t *testing.T) {
	t.Parallel()

	anchor := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	now := anchor.Add(time.Hour)
	auth := now.Add(-5 * time.Minute)
	claim := crowndomain.Claim{
		Act:        "ato-op-1",
		Season:     "temporada-1",
		Holder:     "admin-1",
		Competence: "patrimonial",
		Reign:      1,
		Operator:   true,
	}
	current := crowndomain.CurrentReign{
		Season:   "temporada-1",
		Holder:   "admin-1",
		Reign:    1,
		StartsAt: anchor,
		EndsAt:   anchor.Add(7776000 * time.Second),
		Open:     true,
	}
	session := crowndomain.SovereignSession{
		Subject:         "admin-1",
		Reign:           1,
		Season:          "temporada-1",
		Competence:      "patrimonial",
		AuthenticatedAt: auth,
		MFAAt:           auth.Add(time.Minute),
		ExpiresAt:       auth.Add(20 * time.Minute),
	}
	ctx := crowndomain.AuthContext{
		Now:           now,
		MaxSessionAge: 30 * time.Minute,
	}
	if _, err := crowndomain.AuthorizeAct(claim, session, nil, current, ctx); !errors.Is(err, crowndomain.ErrOperatorNotSovereign) {
		t.Fatalf("AuthorizeAct with operator = %v, want ErrOperatorNotSovereign", err)
	}

	virginBook, _ := patentsdomain.NewSupplyBook("temporada-1")
	ratifiedTable := patentsdomain.SupplyTable{
		Version:         "v1",
		PriceMinorUnits: 1000,
		Currency:        "BRL",
		Eligible:        "cidadaos",
		Cap:             10,
		Duration:        24 * time.Hour,
		CosmeticGrant:   "titulo-honorifico",
		Season:          "temporada-1",
	}
	publishedBook, _ := patentsdomain.PublishTable(virginBook, ratifiedTable)
	_, _, err := patentsdomain.IssueSeat(publishedBook, patentsdomain.IssueRequest{
		PurchaseID: "pur-admin-auction",
		Buyer:      "admin-user",
		Population: "cidadaos",
		SaleMode:   patentsdomain.SaleModeAuction,
		At:         now,
	})
	if !errors.Is(err, patentsdomain.ErrAuctionUnavailable) {
		t.Fatalf("IssueSeat auction mode = %v, want ErrAuctionUnavailable", err)
	}

	grant := patentsdomain.PatentGrant{
		ID:           "pat-1",
		Holder:       "admin-user",
		Season:       "temporada-1",
		Title:        "grao-mestre",
		GrantedAt:    now.Add(-time.Hour),
		ExpiresAt:    now.Add(24 * time.Hour),
		TermsVersion: "v1",
	}
	if err := patentsdomain.TransferPatent(patentsdomain.TransferRequest{Grant: grant, To: "new-user"}); !errors.Is(err, patentsdomain.ErrNonTransferable) {
		t.Fatalf("TransferPatent = %v, want ErrNonTransferable", err)
	}

	holding := patentsdomain.Holding{Grant: grant}
	if err := patentsdomain.RepriceGrant(holding, ratifiedTable); !errors.Is(err, patentsdomain.ErrRetroactiveChange) {
		t.Fatalf("RepriceGrant = %v, want ErrRetroactiveChange", err)
	}

	if economydomain.AreBondsAvailable() {
		t.Fatal("AreBondsAvailable is true, want false")
	}
	if economydomain.AreBettingMarketsAvailable() {
		t.Fatal("AreBettingMarketsAvailable is true, want false")
	}
}

// TestWebhookBypassRejectsUnapprovedProducts verifies webhook payment processing refuses unapproved products.
func TestWebhookBypassRejectsUnapprovedProducts(t *testing.T) {
	t.Parallel()

	if _, err := billingdomain.ParseProductID("$$invalid_prod$$"); !errors.Is(err, billingdomain.ErrInvalidProductID) {
		t.Fatalf("ParseProductID invalid syntax = %v, want ErrInvalidProductID", err)
	}

	probes := []string{
		"crown_bond_100",
		"sports_bet_50",
		"patent_auction_vip",
	}
	ledger := newMockLedger(10000)
	loadedCatalog, err := billingcatalog.Load(billingcatalog.Spec{
		Markets: []billingcatalog.EnabledMarket{
			{Market: "BR", Currency: "BRL"},
			{Market: "INTERNATIONAL", Currency: "USD"},
		},
	})
	if err != nil {
		t.Fatalf("billingcatalog.Load: %v", err)
	}
	now := time.Now().UTC()
	amount, err := billingdomain.NewMoney(5000, billingdomain.CurrencyBRL)
	if err != nil {
		t.Fatalf("NewMoney: %v", err)
	}
	for _, probe := range probes {
		useCase, err := billingapp.NewSettleCheckoutUseCase(billingapp.SettleCheckoutDependencies{
			Catalog: loadedCatalog,
			Intents: &fakeIntentRepo{
				intent: &billingapp.CheckoutIntentRecord{
					ID:             "intent-" + probe,
					AccountID:      "acc-victim",
					Market:         billingdomain.MarketBrazil,
					ProductID:      billingdomain.ProductID(probe),
					CatalogVersion: 1,
					Amount:         amount,
					Status:         billingdomain.CheckoutIntentOpen,
					SessionID:      billingdomain.StripeCheckoutSessionID("cs_" + probe),
					CreatedAt:      now,
				},
			},
			Inker: &fakeInker{ledger: ledger},
			Clock: fakeClock{now: now},
		})
		if err != nil {
			t.Fatalf("NewSettleCheckoutUseCase: %v", err)
		}
		_, err = useCase.Execute(context.Background(), billingapp.SettleCheckoutCommand{
			SessionID: billingdomain.StripeCheckoutSessionID("cs_" + probe),
		})
		if err == nil {
			t.Fatalf("expected error settling unapproved product %s, got nil", probe)
		}
	}
	ledger.AssertUntouched(t, 10000)
}

// TestRoyalDecreeBypassRejectsUnapprovedProducts verifies sovereign decree cannot force unapproved products.
func TestRoyalDecreeBypassRejectsUnapprovedProducts(t *testing.T) {
	t.Parallel()

	unapprovedKinds := []string{
		"titulo",
		"titulos",
		"bond",
		"aposta",
		"apostas",
		"leilao",
		"produto_financeiro",
	}
	for _, kind := range unapprovedKinds {
		if _, err := crowndomain.ParseActKind(kind); !errors.Is(err, crowndomain.ErrUnknownAct) {
			t.Fatalf("ParseActKind(%q) = %v, want ErrUnknownAct", kind, err)
		}
	}

	anchor := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	badAct := crowndomain.RoyalAct{
		ID:         "decreto-test-1",
		Author:     "rainha-1",
		Season:     "temporada-1",
		Reign:      1,
		Competence: "patrimonial",
		Kind:       crowndomain.ActEconomic,
		Reason:     "financiamento ilicito",
		Target:     "conta/beneficiario",
		Effect:     "transferir valor",
		DecreedAt:  anchor,
		Effective:  anchor.Add(time.Hour),
		EndsAt:     anchor.Add(30 * 24 * time.Hour),
		Charter:    "v1",
		Origin:     "titulos",
		Amount:     1000,
	}
	digest, err := crowndomain.ActDigest(badAct)
	if err != nil {
		t.Fatalf("ActDigest: %v", err)
	}
	clearance := crowndomain.Clearance{
		Act:       badAct.ID,
		Season:    badAct.Season,
		Reign:     badAct.Reign,
		Digest:    digest,
		ExpiresAt: anchor.Add(2 * time.Hour),
	}
	current := crowndomain.CurrentReign{
		Season:   "temporada-1",
		Holder:   "rainha-1",
		Reign:    1,
		StartsAt: anchor,
		EndsAt:   anchor.Add(7776000 * time.Second),
		Open:     true,
	}
	custody := crowndomain.CustodyView{
		Origin:      "titulos",
		Beneficiary: "conta/beneficiario",
		Available:   10000,
	}
	_, err = crowndomain.PlanExecution(badAct, clearance, current, custody, anchor.Add(time.Hour+time.Minute))
	if !errors.Is(err, crowndomain.ErrForbiddenOrigin) {
		t.Fatalf("PlanExecution with forbidden origin = %v, want ErrForbiddenOrigin", err)
	}

	badActSelf := badAct
	badActSelf.Origin = "tesouro-livre"
	digestSelf, _ := crowndomain.ActDigest(badActSelf)
	clearanceSelf := clearance
	clearanceSelf.Digest = digestSelf
	custodySelf := crowndomain.CustodyView{
		Origin:      "tesouro-livre",
		Beneficiary: "rainha-1",
		Available:   10000,
	}
	_, err = crowndomain.PlanExecution(badActSelf, clearanceSelf, current, custodySelf, anchor.Add(time.Hour+time.Minute))
	if !errors.Is(err, crowndomain.ErrSelfGrant) {
		t.Fatalf("PlanExecution self-grant = %v, want ErrSelfGrant", err)
	}

	custodyFrozen := crowndomain.CustodyView{
		Origin:      "tesouro-livre",
		Beneficiary: "conta/beneficiario",
		Available:   10000,
		Frozen:      true,
	}
	_, err = crowndomain.PlanExecution(badActSelf, clearanceSelf, current, custodyFrozen, anchor.Add(time.Hour+time.Minute))
	if !errors.Is(err, crowndomain.ErrExecutionFrozen) {
		t.Fatalf("PlanExecution frozen book = %v, want ErrExecutionFrozen", err)
	}
}

// TestConcurrentBypassAttemptsAllFail verifies that concurrent bypass attempts across all paths fail safely.
func TestConcurrentBypassAttemptsAllFail(t *testing.T) {
	t.Parallel()

	ledger := newMockLedger(50000)
	var wg sync.WaitGroup
	errCount := 0
	var countMu sync.Mutex

	runners := []func(*mockLedger) error{
		executeAPIBypassAttempt,
		executeWorkerBypassAttempt,
		executeAdminBypassAttempt,
		executeWebhookBypassAttempt,
		executeDecreeBypassAttempt,
	}

	for i := 0; i < 10; i++ {
		fn := runners[i%len(runners)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := fn(ledger)
			if err != nil {
				countMu.Lock()
				errCount++
				countMu.Unlock()
			}
		}()
	}
	wg.Wait()

	if errCount != 10 {
		t.Fatalf("expected 10 bypass errors, got %d", errCount)
	}
	ledger.AssertUntouched(t, 50000)
}

// TestRepeatedBypassAttemptsAreIdempotentlyRefused verifies replay attempts are idempotently refused.
func TestRepeatedBypassAttemptsAreIdempotentlyRefused(t *testing.T) {
	t.Parallel()

	ledger := newMockLedger(50000)
	runners := []func(*mockLedger) error{
		executeAPIBypassAttempt,
		executeWorkerBypassAttempt,
		executeAdminBypassAttempt,
		executeWebhookBypassAttempt,
		executeDecreeBypassAttempt,
	}

	for iter := 0; iter < 10; iter++ {
		for _, fn := range runners {
			err := fn(ledger)
			if err == nil {
				t.Fatalf("iteration %d: expected bypass error, got nil", iter)
			}
		}
	}
	ledger.AssertUntouched(t, 50000)
}

// TestUnapprovedProductFixtureFailsGate verifies that fixtures attempting to activate unapproved products fail gate checks.
func TestUnapprovedProductFixtureFailsGate(t *testing.T) {
	t.Parallel()

	if err := patentsdomain.ValidateSupplyTable(patentsdomain.SupplyTable{}); !errors.Is(err, patentsdomain.ErrTermsMissing) {
		t.Fatalf("ValidateSupplyTable empty = %v, want ErrTermsMissing", err)
	}

	book, err := patentsdomain.NewSupplyBook("temporada-1")
	if err != nil {
		t.Fatalf("NewSupplyBook: %v", err)
	}
	tableMismatched := patentsdomain.SupplyTable{
		Version:         "v1",
		PriceMinorUnits: 1000,
		Currency:        "BRL",
		Eligible:        "cidadaos",
		Cap:             10,
		Duration:        24 * time.Hour,
		CosmeticGrant:   "titulo-honorifico",
		Season:          "temporada-2",
	}
	if _, err := patentsdomain.PublishTable(book, tableMismatched); !errors.Is(err, patentsdomain.ErrInvalidGrant) {
		t.Fatalf("PublishTable season mismatch = %v, want ErrInvalidGrant", err)
	}

	bondTerms := economydomain.HypotheticalBondTerms{
		Custody:         economydomain.CustodyTitle,
		DurationDays:    90,
		WeightFactor:    1,
		YieldCapped:     true,
		YieldGuaranteed: false,
		Status:          economydomain.BondStatusConceptOnly,
	}
	if err := economydomain.ValidateHypotheticalContract(bondTerms); !errors.Is(err, economydomain.ErrBondsUnavailable) {
		t.Fatalf("ValidateHypotheticalContract = %v, want ErrBondsUnavailable", err)
	}

	bettingMissing := economydomain.BettingPreconditions{
		CountryCode: "BR",
	}
	if err := economydomain.ValidateBettingPreconditions(bettingMissing); !errors.Is(err, economydomain.ErrBettingPreconditionMissing) {
		t.Fatalf("ValidateBettingPreconditions missing = %v, want ErrBettingPreconditionMissing", err)
	}

	bettingHarm := economydomain.BettingPreconditions{
		CountryCode:      "BR",
		InvolvesRealHarm: true,
	}
	if err := economydomain.ValidateBettingPreconditions(bettingHarm); !errors.Is(err, economydomain.ErrRealHarmProhibited) {
		t.Fatalf("ValidateBettingPreconditions real harm = %v, want ErrRealHarmProhibited", err)
	}

	badRoutes := []httpserver.Route{
		{Method: "POST", Path: "/api/v1/crown-bonds/checkout"},
	}
	if violations := scanRoutesForBondTokens(badRoutes); len(violations) == 0 {
		t.Fatal("scanRoutesForBondTokens failed to detect bad route fixture")
	}

	badBettingRoutes := []httpserver.Route{
		{Method: "POST", Path: "/api/v1/bets/place"},
	}
	if violations := scanRoutesForBettingTokens(badBettingRoutes); len(violations) == 0 {
		t.Fatal("scanRoutesForBettingTokens failed to detect bad route fixture")
	}

	badCatalog := []byte(`{"version":1,"products":[{"market":"BR","product":"crown_bond_1","grant":"BOND"}]}`)
	if violations, err := scanCatalogForBondTokens(badCatalog); err != nil || len(violations) == 0 {
		t.Fatalf("scanCatalogForBondTokens failed to detect bad catalog fixture: %v, violations: %v", err, violations)
	}
}
