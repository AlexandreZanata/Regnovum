/**
 * Tests of the wallet presentation (P54-T01).
 *
 * They run the real generated catalogs in both locales: the
 * balances render whole INK units with the locale's grouping, an
 * unsafe integer is refused instead of rounded, one ledger line
 * renders its signed amount with the instant localized, and
 * failures name only the server codes the backend really emits.
 * Nothing here splits legacy from seasonal: the contract carries
 * two buckets and the page renders those two, never a third.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  assertSafeInk,
  balancePresentation,
  mergeStatementEntries,
  statementCursor,
  statementEmpty,
  statementEntryView,
  statementLimit,
  walletFailure,
} from "../../src/pages/wallet.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { WalletBalance, WalletStatementEntry } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the wallet views render from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["wallet"] });
}

const BALANCE: WalletBalance = { balance_free: 3800, balance_purchased: 300 };

const ENTRY: WalletStatementEntry = {
  transaction_id: "tx-1",
  operation_id: "op-1",
  operation_type: "debit_argument",
  bucket: "FREE_INK",
  amount: -100,
  reference: "arena:arg-1",
  created_at: "2026-10-01T10:00:00Z",
};

test("the balances render whole units with the locale grouping", () => {
  const view = balancePresentation(translatorOf("pt-BR"), "pt-BR", BALANCE);

  assert.equal(view.free, "INK livre: 3.800");
  assert.equal(view.purchased, "INK comprado: 300");
  assert.equal(view.total, "Total: 4.100");

  const en = balancePresentation(translatorOf("en-US"), "en-US", BALANCE);
  assert.equal(en.free, "Free INK: 3,800");
  assert.equal(en.purchased, "Purchased INK: 300");
  assert.equal(en.total, "Total: 4,100");
});

test("zero and large balances render without losing a unit", () => {
  const zero = balancePresentation(translatorOf("en-US"), "en-US", {
    balance_free: 0,
    balance_purchased: 0,
  });
  assert.equal(zero.total, "Total: 0");

  const large = balancePresentation(translatorOf("en-US"), "en-US", {
    balance_free: Number.MAX_SAFE_INTEGER - 7,
    balance_purchased: 7,
  });
  assert.equal(large.total, `Total: ${(Number.MAX_SAFE_INTEGER).toLocaleString("en-US")}`);
});

test("an unsafe integer is refused instead of rounded", () => {
  assert.throws(() => assertSafeInk(Number.MAX_SAFE_INTEGER + 1), TypeError);
  assert.throws(() => assertSafeInk(1.5), TypeError);
  assert.throws(
    () => balancePresentation(translatorOf("en-US"), "en-US", { balance_free: 1.5, balance_purchased: 0 }),
    TypeError,
  );
});

test("one ledger line renders its signed amount with no raw instant", () => {
  const view = statementEntryView(translatorOf("pt-BR"), "pt-BR", ENTRY);

  assert.ok(view.line.includes("-100"), "signed amount missing");
  assert.ok(view.line.includes("debit_argument"), "operation vocabulary missing");
  assert.ok(view.line.includes("FREE_INK"), "bucket vocabulary missing");
  assert.ok(!view.line.includes("2026-10-01T10:00:00Z"), "raw instant leaked");
  assert.equal(view.reference, "arena:arg-1");

  const serialized = JSON.stringify(view).toLowerCase();
  for (const marker of ["legacy", "season", "receipt", "float", "price", "money"]) {
    assert.ok(!serialized.includes(marker), `invented marker leaked: ${marker}`);
  }
});

test("credits render with an explicit plus sign", () => {
  const credit: WalletStatementEntry = { ...ENTRY, transaction_id: "tx-2", amount: 5000, operation_type: "credit_free" };
  const view = statementEntryView(translatorOf("en-US"), "en-US", credit);
  assert.ok(view.line.includes("+5,000"), `credit sign missing: ${view.line}`);
});

test("the empty ledger and the merge keep one page honest", () => {
  assert.equal(statementEmpty(translatorOf("en-US")), "No entries yet");
  assert.equal(statementEmpty(translatorOf("pt-BR")), "Nenhum lançamento ainda");

  const second: WalletStatementEntry = { ...ENTRY, transaction_id: "tx-2" };
  assert.deepEqual(
    mergeStatementEntries([ENTRY], [ENTRY, second]).map((entry) => entry.transaction_id),
    ["tx-1", "tx-2"],
  );
});

test("the cursor travels opaque and the limit only inside 1..100", () => {
  assert.equal(statementCursor(null), undefined);
  assert.equal(statementCursor(""), undefined);
  assert.equal(statementCursor("opaque"), "opaque");
  assert.equal(statementLimit(null), undefined);
  assert.equal(statementLimit("20"), 20);
  assert.equal(statementLimit("0"), undefined);
  assert.equal(statementLimit("101"), undefined);
  assert.equal(statementLimit("abc"), undefined);
});

test("wallet failures name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(walletFailure(translator, "invalid_cursor").includes("cursor"));
  assert.ok(walletFailure(translator, "invalid_limit").includes("page size"));
  assert.ok(walletFailure(translator, "unauthorized").includes("Sign in"));
  assert.equal(
    walletFailure(translator, "something-the-backend-never-emits"),
    walletFailure(translator, "invalid_cursor".replace("invalid_cursor", "unknown-code")),
    "an unknown code must fall back to the generic sentence",
  );
});
