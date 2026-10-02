package domain

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func supplyTable() SupplyTable {
	return SupplyTable{
		Version: "tabela-v1", PriceMinorUnits: 5000, Currency: "BRL",
		Eligible: "membros-ativos", Cap: 3, Duration: 30 * 24 * time.Hour,
		CosmeticGrant: "filete dourado no perfil", Season: "temporada-1",
	}
}

func supplyBook(t *testing.T) SupplyBook {
	t.Helper()
	book, err := NewSupplyBook("temporada-1")
	if err != nil {
		t.Fatalf("NewSupplyBook: %v", err)
	}
	book, err = PublishTable(book, supplyTable())
	if err != nil {
		t.Fatalf("PublishTable: %v", err)
	}
	return book
}

func issueReq(id, buyer string) IssueRequest {
	return IssueRequest{
		PurchaseID: id, Buyer: buyer, Population: "membros-ativos",
		SaleMode: SaleModeDirect, At: patentAnchor(),
	}
}

func TestRatifiedTableSellsUpToItsCap(t *testing.T) {
	terms, err := TermsOf(supplyTable())
	if err != nil {
		t.Fatalf("TermsOf: %v", err)
	}
	if terms.PriceMinorUnits != 5000 || terms.Currency != "BRL" || terms.Seats != 3 {
		t.Fatalf("terms = %+v, want exactly what the table approves", terms)
	}
	book := supplyBook(t)
	for i, buyer := range []string{"ana", "bruno", "carlos"} {
		var seat IssuedSeat
		book, seat, err = IssueSeat(book, issueReq("compra-"+buyer, buyer))
		if err != nil {
			t.Fatalf("IssueSeat %s: %v", buyer, err)
		}
		if seat.Position != int64(i+1) || seat.Buyer != buyer {
			t.Fatalf("seat = %+v, want position %d for %s", seat, i+1, buyer)
		}
	}
	replayBook, replay, err := IssueSeat(book, issueReq("compra-ana", "ana"))
	if err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if replay.Position != 1 || len(replayBook.Seats) != 3 {
		t.Fatalf("replay = %+v, want the same seat without a new one", replay)
	}
}

func TestMissingPriceOrCapBlocksIssue(t *testing.T) {
	book, err := NewSupplyBook("temporada-1")
	if err != nil {
		t.Fatalf("NewSupplyBook: %v", err)
	}
	if _, _, err := IssueSeat(book, issueReq("compra-ana", "ana")); !errors.Is(err, ErrTermsMissing) {
		t.Fatalf("issue without a table = %v, want ErrTermsMissing", err)
	}
	if _, err := NewSupplyBook(" "); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("blank season book = %v, want ErrInvalidGrant", err)
	}
	spoils := []func(*SupplyTable){
		func(table *SupplyTable) { table.PriceMinorUnits = 0 },
		func(table *SupplyTable) { table.Cap = 0 },
		func(table *SupplyTable) { table.Eligible = "" },
		func(table *SupplyTable) { table.Currency = "" },
		func(table *SupplyTable) { table.Version = "" },
		func(table *SupplyTable) { table.Duration = 0 },
	}
	for i, spoil := range spoils {
		table := supplyTable()
		spoil(&table)
		if _, err := PublishTable(book, table); !errors.Is(err, ErrTermsMissing) {
			t.Fatalf("missing term %d = %v, want ErrTermsMissing", i, err)
		}
	}
}

func TestOversubscriptionAndStrangersRefuse(t *testing.T) {
	book := supplyBook(t)
	for _, buyer := range []string{"ana", "bruno", "carlos"} {
		var err error
		book, _, err = IssueSeat(book, issueReq("compra-"+buyer, buyer))
		if err != nil {
			t.Fatalf("IssueSeat %s: %v", buyer, err)
		}
	}
	if _, _, err := IssueSeat(book, issueReq("compra-diana", "diana")); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("past the cap = %v, want ErrCapExceeded", err)
	}
	if len(book.Seats) != 3 {
		t.Fatalf("book holds %d seats, want the cap to stay exact", len(book.Seats))
	}
	small := supplyBook(t)
	stranger := issueReq("compra-eva", "eva")
	stranger.Population = "visitantes"
	if _, _, err := IssueSeat(small, stranger); !errors.Is(err, ErrIneligibleBuyer) {
		t.Fatalf("stranger population = %v, want ErrIneligibleBuyer", err)
	}
	taken := issueReq("compra-ana", "bruno")
	book2 := supplyBook(t)
	var err error
	book2, _, err = IssueSeat(book2, issueReq("compra-ana", "ana"))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, _, err := IssueSeat(book2, taken); !errors.Is(err, ErrDuplicateHold) {
		t.Fatalf("reused purchase identity = %v, want ErrDuplicateHold", err)
	}
	unknown := issueReq("compra-ana", "ana")
	unknown.SaleMode = "permuta"
	if _, _, err := IssueSeat(supplyBook(t), unknown); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("unknown sale mode = %v, want ErrInvalidGrant", err)
	}
}

func TestPastNeverReprices(t *testing.T) {
	book := supplyBook(t)
	var err error
	book, _, err = IssueSeat(book, issueReq("compra-ana", "ana"))
	if err != nil {
		t.Fatalf("IssueSeat: %v", err)
	}
	altered := supplyTable()
	altered.PriceMinorUnits = 9000
	if _, err := PublishTable(book, altered); !errors.Is(err, ErrRetroactiveChange) {
		t.Fatalf("same version new price = %v, want ErrRetroactiveChange", err)
	}
	republished, err := PublishTable(book, supplyTable())
	if err != nil {
		t.Fatalf("identical republication: %v", err)
	}
	if len(republished.Seats) != 1 {
		t.Fatalf("republished holds %d seats, want issued seats kept", len(republished.Seats))
	}
	next := supplyTable()
	next.Version = "tabela-v2"
	next.PriceMinorUnits = 7000
	moved, err := PublishTable(book, next)
	if err != nil {
		t.Fatalf("new version: %v", err)
	}
	if moved.Table.PriceMinorUnits != 7000 || len(moved.Seats) != 1 {
		t.Fatalf("moved = %+v, want future sales repriced with past seats kept", moved)
	}
	holding := acquirePatent(t, patentRequest())
	if err := RepriceGrant(holding, next); !errors.Is(err, ErrRetroactiveChange) {
		t.Fatalf("reprice = %v, want ErrRetroactiveChange", err)
	}
	if err := RepriceGrant(Holding{}, next); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("reprice of nothing = %v, want ErrInvalidGrant", err)
	}
}

func TestFreezeEndsSalesWithoutThaw(t *testing.T) {
	book := supplyBook(t)
	var err error
	book, _, err = IssueSeat(book, issueReq("compra-ana", "ana"))
	if err != nil {
		t.Fatalf("IssueSeat: %v", err)
	}
	at := patentAnchor().Add(time.Hour)
	book, err = FreezeSales(FreezeRequest{Book: book, At: at, Reason: "revisao do teto pelo titular"})
	if err != nil {
		t.Fatalf("FreezeSales: %v", err)
	}
	if _, _, err := IssueSeat(book, issueReq("compra-bruno", "bruno")); !errors.Is(err, ErrFrozenSale) {
		t.Fatalf("sale during freeze = %v, want ErrFrozenSale", err)
	}
	next := supplyTable()
	next.Version = "tabela-v2"
	if _, err := PublishTable(book, next); !errors.Is(err, ErrFrozenSale) {
		t.Fatalf("publish over a frozen book = %v, want ErrFrozenSale", err)
	}
	replay, err := FreezeSales(FreezeRequest{Book: book, At: at, Reason: "revisao do teto pelo titular"})
	if err != nil {
		t.Fatalf("identical freeze replay: %v", err)
	}
	if !replay.Table.Frozen || len(replay.Seats) != 1 {
		t.Fatalf("replay = %+v, want the same frozen book with its seat", replay)
	}
	if _, err := FreezeSales(FreezeRequest{Book: book, At: at.Add(time.Hour), Reason: "outro motivo"}); !errors.Is(err, ErrFrozenSale) {
		t.Fatalf("second freeze fact = %v, want ErrFrozenSale", err)
	}
	virgin, err := NewSupplyBook("temporada-1")
	if err != nil {
		t.Fatalf("NewSupplyBook: %v", err)
	}
	if _, err := FreezeSales(FreezeRequest{Book: virgin, At: at, Reason: "revisao do teto pelo titular"}); !errors.Is(err, ErrTermsMissing) {
		t.Fatalf("freeze without a table = %v, want ErrTermsMissing", err)
	}
	holding := acquirePatent(t, patentRequest())
	if err := CheckUse(UseRequest{Holding: holding, At: patentAnchor()}); err != nil {
		t.Fatalf("past sales stay valid under freeze: %v", err)
	}
}

func TestAuctionHasNoEndpoint(t *testing.T) {
	book := supplyBook(t)
	bid := issueReq("compra-ana", "ana")
	bid.SaleMode = SaleModeAuction
	if _, _, err := IssueSeat(book, bid); !errors.Is(err, ErrAuctionUnavailable) {
		t.Fatalf("auction bid = %v, want ErrAuctionUnavailable", err)
	}
	if len(book.Seats) != 0 {
		t.Fatalf("book holds %d seats after a refused bid, want none", len(book.Seats))
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller cannot locate this test")
	}
	entries, err := os.ReadDir(filepath.Dir(file))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	allowed := map[string]bool{
		"ErrAuctionUnavailable": true, "CodeAuctionUnavailable": true, "SaleModeAuction": true,
	}
	found := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(file), name), nil, 0)
		if parseErr != nil {
			t.Fatalf("ParseFile %s: %v", name, parseErr)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			ident, isIdent := node.(*ast.Ident)
			if !isIdent || allowed[ident.Name] {
				return true
			}
			lowered := strings.ToLower(ident.Name)
			for _, hint := range []string{"auction", "leilao", "bid", "lance"} {
				if strings.Contains(lowered, hint) {
					found = append(found, name+":"+ident.Name)
				}
			}
			return true
		})
	}
	if len(found) > 0 {
		t.Fatalf("auction surface outside the refusal: %v", found)
	}
}
