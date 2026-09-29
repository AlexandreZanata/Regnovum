package http_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestTradeJourneyReceiptStatement(t *testing.T) {
	h, _ := newCommerceHarness(t)

	receipt := commerceRequest(t, h, http.MethodGet, "/api/v1/me/commerce/contracts/"+h.contractID,
		commerceOwnerToken, "pt-BR")
	if receipt.Code != http.StatusOK {
		t.Fatalf("receipt status = %d, body %s", receipt.Code, receipt.Body.String())
	}
	assertCommerceNoStore(t, receipt)
	document := commerceDecode(t, receipt.Body.Bytes())
	if document["gross_milli"] != float64(20000) || document["tithe_milli"] != float64(2000) || document["net_milli"] != float64(18000) {
		t.Fatalf("receipt = %v, want gross 20000 with tithe 2000 and net 18000", document)
	}
	if document["status"] != "released" || document["role"] != "buyer" {
		t.Fatalf("receipt = %v, want released through the buyer side", document)
	}
	for _, key := range []string{"title", "contract_id", "contract_key", "role", "object", "gross_milli", "tithe_milli", "net_milli", "status", "expires_at", "posted_at", "settled_at", "escrow_transfer_id", "settlement_transfer_id", "refunds", "refunded_milli"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("receipt missing key %q: %v", key, document)
		}
	}

	statement := commerceRequest(t, h, http.MethodGet, "/api/v1/me/commerce/statement",
		commerceOwnerToken, "pt-BR")
	if statement.Code != http.StatusOK {
		t.Fatalf("statement status = %d, body %s", statement.Code, statement.Body.String())
	}
	assertCommerceNoStore(t, statement)
	extract := commerceDecode(t, statement.Body.Bytes())
	entries, _ := extract["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %v, want one owned line", extract["entries"])
	}
}

func TestTradeAPIRequiresAuthentication(t *testing.T) {
	h, _ := newCommerceHarness(t)
	paths := []string{
		"/api/v1/me/commerce/contracts/" + h.contractID,
		"/api/v1/me/commerce/statement",
	}
	for _, path := range paths {
		recorder := commerceRequest(t, h, http.MethodGet, path, "", "")
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s = %d, want 401", path, recorder.Code)
		}
	}
}

func TestTradeForeignReceiptIsNotFound(t *testing.T) {
	h, _ := newCommerceHarness(t)
	alien := commerceRequest(t, h, http.MethodGet, "/api/v1/me/commerce/contracts/00000000-0000-4000-8000-000000000000",
		commerceOwnerToken, "")
	if alien.Code != http.StatusNotFound {
		t.Fatalf("unknown receipt = %d, want 404", alien.Code)
	}
}

func TestTradeBothSidesReadIdenticalAmounts(t *testing.T) {
	h, _ := newCommerceHarness(t)
	buyer := commerceDecode(t, commerceRequest(t, h, http.MethodGet,
		"/api/v1/me/commerce/contracts/"+h.contractID, commerceOwnerToken, "pt-BR").Body.Bytes())
	provider := commerceDecode(t, commerceRequest(t, h, http.MethodGet,
		"/api/v1/me/commerce/contracts/"+h.contractID, commerceOtherToken, "pt-BR").Body.Bytes())
	for _, key := range []string{"gross_milli", "tithe_milli", "net_milli", "status", "contract_key"} {
		if buyer[key] != provider[key] {
			t.Fatalf("sides diverge on %q: buyer=%v provider=%v; both sides read the same contract", key, buyer[key], provider[key])
		}
	}
	if buyer["role"] == provider["role"] {
		t.Fatalf("roles do not differ: buyer=%v provider=%v", buyer["role"], provider["role"])
	}
}

func TestTradeReceiptIsLocaleIndependent(t *testing.T) {
	h, _ := newCommerceHarness(t)
	pt := commerceDecode(t, commerceRequest(t, h, http.MethodGet,
		"/api/v1/me/commerce/contracts/"+h.contractID, commerceOwnerToken, "pt-BR").Body.Bytes())
	en := commerceDecode(t, commerceRequest(t, h, http.MethodGet,
		"/api/v1/me/commerce/contracts/"+h.contractID, commerceOwnerToken, "en-US").Body.Bytes())
	for _, key := range []string{"gross_milli", "tithe_milli", "net_milli", "status", "contract_key", "refunded_milli"} {
		if pt[key] != en[key] {
			t.Fatalf("locale changed %q: pt=%v en=%v; translated text must never enter the computation", key, pt[key], en[key])
		}
	}
	if pt["title"] == en["title"] {
		t.Fatalf("titles do not differ: pt=%v en=%v", pt["title"], en["title"])
	}
	if !strings.Contains(pt["title"].(string), "cio") || !strings.Contains(en["title"].(string), "ceipt") {
		t.Fatalf("titles lost the receipt: pt=%v en=%v", pt["title"], en["title"])
	}
}
