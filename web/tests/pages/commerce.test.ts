/**
 * Tests of the commerce staged presentation (P57-T03).
 *
 * They run the real generated catalogs in both locales: one receipt
 * renders parties, status and the tithe split with no added rate;
 * both sides read identical amounts through their own role; the
 * terminal policy is explained without deciding litigation; the
 * extract keeps the server page with no invented cursor; and
 * failures name only the server codes the backend really emits. A
 * disabled capability renders unavailability and sends nothing.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import type { TradeReceipt, TradeStatement } from "../../src/contracts/staged/commerce.js";
import { noStagedCapabilities, stagedCapabilities } from "../../src/core/staged.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { Locale } from "../../src/i18n/locale.js";
import {
  commerceFailure,
  commerceGate,
  isTerminalTradeStatus,
  receiptView,
  requireCommerceEnabled,
  statementView,
  terminalNote,
} from "../../src/pages/commerce.js";
import { StagedUnavailableError } from "../../src/pages/staged.js";

/** A translator holding exactly the namespace the page renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["commerce"] });
}

const RECEIPT: TradeReceipt = {
  contract_id: "00000000-0000-4000-8000-000000000002",
  contract_key: "contrato-1",
  escrow_transfer_id: "escrow-1",
  expires_at: "2026-11-05T00:00:00Z",
  gross_milli: 20000,
  net_milli: 18000,
  object: "serviço com recibo",
  posted_at: "2026-10-05T10:00:00Z",
  refunded_milli: 0,
  refunds: [],
  role: "buyer",
  settled_at: "2026-10-06T10:00:00Z",
  settlement_transfer_id: "liquidacao-1",
  status: "released",
  tithe_milli: 2000,
  title: "Recibo de comércio",
};

test("one receipt renders parties, status and the split with no added rate", () => {
  for (const locale of ["pt-BR", "en-US"] as const) {
    const view = receiptView(translatorOf(locale), locale, RECEIPT);

    assert.equal(view.key, "contrato-1");
    assert.ok(view.role.includes("buyer"), "party role missing");
    assert.equal(view.object, "serviço com recibo");
    assert.ok(view.gross.includes("20.000") || view.gross.includes("20,000"), `gross misread: ${view.gross}`);
    assert.ok(view.tithe.includes("2.000") || view.tithe.includes("2,000"), `tithe misread: ${view.tithe}`);
    assert.ok(view.net.includes("18.000") || view.net.includes("18,000"), `net misread: ${view.net}`);
    assert.ok(view.status.includes("released"), "verbatim status missing");
    assert.ok(!view.posted.includes("2026-10-05T10:00:00Z"), "raw instant leaked");
    assert.ok(view.settled !== null, "settled instant missing on a released contract");
    assert.equal(view.escrow, "escrow-1");
    const serialized = JSON.stringify(view).toLowerCase();
    for (const marker of ["interest", "juros", "market", "mercado", "crown", "coroa", "fee", "taxa"]) {
      assert.ok(!serialized.includes(marker), `receipt added ${marker}`);
    }
  }
});

test("both sides read identical amounts through their own role", () => {
  const buyer = receiptView(translatorOf("pt-BR"), "pt-BR", RECEIPT);
  const provider = receiptView(translatorOf("pt-BR"), "pt-BR", { ...RECEIPT, role: "provider" });

  assert.equal(buyer.gross, provider.gross);
  assert.equal(buyer.tithe, provider.tithe);
  assert.equal(buyer.net, provider.net);
  assert.ok(buyer.role.includes("buyer"));
  assert.ok(provider.role.includes("provider"));
  assert.ok(!buyer.role.includes("provider") && !provider.role.includes("buyer"), "roles must differ by side");
});

test("the terminal policy is explained without deciding litigation", () => {
  const translator = translatorOf("en-US");

  for (const status of ["released", "refunded", "resolved"]) {
    assert.equal(isTerminalTradeStatus(status), true);
    assert.ok(terminalNote(translator, status).includes("no new effect"), `${status} must keep the no-effect note`);
  }
  for (const status of ["funded", "accepted", "expired"]) {
    assert.equal(isTerminalTradeStatus(status), false);
    assert.ok(
      terminalNote(translator, status).includes("competent authority"),
      `${status} must keep the authority note`,
    );
  }
  assert.ok(terminalNote(translator, "something-undecidable").includes("custody preserved"), "unknown must block");
  const serialized = JSON.stringify([
    terminalNote(translator, "released"),
    terminalNote(translator, "funded"),
    terminalNote(translator, "unknown"),
  ]).toLowerCase();
  assert.ok(!serialized.includes("guilty") && !serialized.includes("culpado"), "the page never judges");
});

test("a pending contract carries no settlement instant", () => {
  const { settled_at: _settled, settlement_transfer_id: _settlement, ...pending } = RECEIPT;
  const view = receiptView(translatorOf("pt-BR"), "pt-BR", { ...pending, status: "funded" });

  assert.equal(view.settled, null);
  assert.ok(view.policy.includes("autoridade competente"), "pending must name the competent authority");
});

test("the statement keeps the server page with no invented cursor", () => {
  const statement: TradeStatement = {
    entries: [
      {
        contract_id: "00000000-0000-4000-8000-000000000002",
        contract_key: "contrato-1",
        gross_milli: 20000,
        posted_at: "2026-10-05T10:00:00Z",
        role: "buyer",
        status: "released",
      },
      {
        contract_id: "00000000-0000-4000-8000-000000000003",
        contract_key: "contrato-2",
        gross_milli: 5000,
        posted_at: "2026-10-04T10:00:00Z",
        role: "provider",
        status: "funded",
      },
    ],
    title: "Extrato de comércio",
  };
  const view = statementView(translatorOf("pt-BR"), "pt-BR", statement);

  assert.equal(view.state, "ready");
  if (view.state === "ready") {
    assert.deepEqual(
      view.rows.map((row) => row.key),
      ["contrato-1", "contrato-2"],
      "the extract must keep the server order",
    );
    assert.ok(view.rows[0]?.status.includes("released"));
    assert.ok(view.rows[1]?.status.includes("funded"));
  }

  const empty = statementView(translatorOf("en-US"), "en-US", { entries: [], title: "Trade statement" });
  assert.equal(empty.state, "empty");
});

test("commerce denials name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(commerceFailure(translator, "unauthorized").includes("Sign in"));
  assert.ok(commerceFailure(translator, "contract_unknown").includes("does not exist"));
  assert.ok(commerceFailure(translator, "receipt_invalid").includes("not valid"));
  assert.equal(
    commerceFailure(translator, "something-the-backend-never-emits"),
    commerceFailure(translator, "unknown-code"),
    "an unknown code must fall back to the generic sentence",
  );
});

test("a disabled capability renders unavailability and sends nothing", () => {
  const translator = translatorOf("pt-BR");

  const off = commerceGate(noStagedCapabilities(), translator);
  assert.equal(off.enabled, false);
  assert.ok((off.view?.heading ?? "").includes("indisponível"), "unavailability heading missing");

  const on = commerceGate(stagedCapabilities(["commerce"]), translator);
  assert.equal(on.enabled, true);
  assert.equal(on.view, null);

  assert.throws(() => requireCommerceEnabled(noStagedCapabilities()), StagedUnavailableError);
  requireCommerceEnabled(stagedCapabilities(["commerce"]));
});
