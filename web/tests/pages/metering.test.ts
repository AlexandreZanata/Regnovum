/**
 * Tests of the metering staged presentation (P57-T02).
 *
 * They run the real generated catalogs in both locales: the quote renders
 * exactly what the server priced with no computed price, an altered
 * candidate prices anew, a lapsed window never confirms, the receipt is
 * checked original against the preview terms, the statement carries only
 * the journal-derived balance with no optimistic debit, and failures name
 * only the server codes the backend really emits. A disabled capability
 * renders unavailability and guards the confirmation.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import type {
  MeteringQuote,
  MeteringReceipt,
  MeteringStatement,
} from "../../src/contracts/staged/metering.js";
import { noStagedCapabilities, stagedCapabilities } from "../../src/core/staged.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { Locale } from "../../src/i18n/locale.js";
import {
  assertSafeMilli,
  createConfirmGuard,
  isOriginalReceipt,
  isQuoteLive,
  meteringFailure,
  meteringGate,
  needsFreshQuote,
  publicationView,
  quoteView,
  receiptView,
  requireMeteringEnabled,
  statementView,
} from "../../src/pages/metering.js";
import { StagedUnavailableError } from "../../src/pages/staged.js";

/** A translator holding exactly the namespace the page renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["metering"] });
}

const QUOTE: MeteringQuote = {
  accepted_at: "2026-10-05T10:00:00Z",
  content_hash: "hash-conteudo",
  expires_at: "2026-10-05T10:05:00Z",
  price_milli: 250,
  quote_hash: "hash-cotacao",
  title: "Preço de medição de INK",
  total_milli: 2750,
  units: 11,
  version: 2,
};

const RECEIPT: MeteringReceipt = {
  content_hash: "hash-conteudo",
  legs: [{ amount_milli: 2750, direction: "debit", kind: "metering", label: "argument-publish" }],
  posted_at: "2026-10-05T10:01:00Z",
  publication_id: "00000000-0000-4000-8000-000000000001",
  service: "argument-publish",
  title: "Recibo de INK",
  total_milli: 2750,
  transfer_id: "00000000-0000-4000-8000-000000000002",
  units: 11,
  version: 2,
};

const NOW_LIVE = new Date("2026-10-05T10:02:00Z");
const NOW_LAPSED = new Date("2026-10-05T10:06:00Z");

test("the quote renders exactly what the server priced", () => {
  for (const locale of ["pt-BR", "en-US"] as const) {
    const view = quoteView(translatorOf(locale), locale, QUOTE, NOW_LIVE);

    assert.ok(view.total.includes("2.750") || view.total.includes("2,750"), `total misread: ${view.total}`);
    assert.ok(view.total.includes("11"), "units missing");
    assert.equal(view.versionNumber, 2);
    assert.equal(view.contentHash, "hash-conteudo");
    assert.ok(!view.window.includes("2026-10-05T10:05:00Z"), "raw instant leaked");
    assert.equal(view.live, true);
    assert.equal(view.expiryNote, null);
    const serialized = JSON.stringify(view);
    assert.ok(!serialized.includes("undefined"), "unpriced field leaked");
  }
});

test("a lapsed window never confirms and names a new quote", () => {
  assert.equal(isQuoteLive(QUOTE.expires_at, NOW_LIVE), true);
  assert.equal(isQuoteLive(QUOTE.expires_at, NOW_LAPSED), false);
  assert.equal(isQuoteLive(QUOTE.expires_at, new Date(QUOTE.expires_at)), false, "reaching expiry ends it");

  const live = quoteView(translatorOf("pt-BR"), "pt-BR", QUOTE, NOW_LIVE);
  assert.equal(live.live, true);

  const lapsed = quoteView(translatorOf("pt-BR"), "pt-BR", QUOTE, NOW_LAPSED);
  assert.equal(lapsed.live, false);
  assert.ok((lapsed.expiryNote ?? "").includes("nova cotação"), "lapse must request a new quote");
  assert.equal(lapsed.total, live.total, "a lapse never reprices the preview");

  const en = quoteView(translatorOf("en-US"), "en-US", QUOTE, NOW_LAPSED);
  assert.ok((en.expiryNote ?? "").includes("new quote"), "lapse must request a new quote");
});

test("any altered byte prices anew instead of reusing the preview", () => {
  assert.equal(needsFreshQuote("texto final", "texto final", "argument-publish", "argument-publish"), false);
  assert.equal(needsFreshQuote("texto final", "texto adulterado", "argument-publish", "argument-publish"), true);
  assert.equal(needsFreshQuote("texto final", "texto final", "argument-publish", "other-service"), true);
  assert.equal(needsFreshQuote("texto final", "", "argument-publish", "argument-publish"), true);
});

test("the receipt is checked original against the preview terms", () => {
  assert.equal(isOriginalReceipt(QUOTE, RECEIPT), true);
  assert.equal(isOriginalReceipt(QUOTE, { ...RECEIPT, total_milli: 9999 }), false);
  assert.equal(isOriginalReceipt(QUOTE, { ...RECEIPT, content_hash: "outro-hash" }), false);
  assert.equal(isOriginalReceipt(QUOTE, { ...RECEIPT, version: 99 }), false);

  const view = receiptView(translatorOf("pt-BR"), "pt-BR", RECEIPT);
  assert.equal(view.publicationId, "00000000-0000-4000-8000-000000000001");
  assert.equal(view.legs.length, 1);
  assert.equal(view.legs[0]?.label, "argument-publish");
  assert.equal(view.refund, null);
  assert.ok(!JSON.stringify(view).toLowerCase().includes("float"), "amounts never float");

  const refunded = receiptView(translatorOf("en-US"), "en-US", {
    ...RECEIPT,
    refund: {
      amount_milli: 2750,
      posted_at: "2026-10-06T10:00:00Z",
      refund_id: "00000000-0000-4000-8000-000000000003",
      transfer_id: "00000000-0000-4000-8000-000000000004",
    },
  });
  assert.ok((refunded.refund ?? "").includes("Refund"), "refund line missing");
});

test("the settlement names replay without a second charge", () => {
  const first = publicationView(translatorOf("pt-BR"), "pt-BR", {
    posted_at: "2026-10-05T10:01:00Z",
    publication_id: "00000000-0000-4000-8000-000000000001",
    replayed: false,
    title: "Recibo de INK",
    total_milli: 2750,
    transfer_id: "00000000-0000-4000-8000-000000000002",
  });
  assert.equal(first.replayed, false);

  const replay = publicationView(translatorOf("pt-BR"), "pt-BR", {
    posted_at: "2026-10-05T10:01:00Z",
    publication_id: "00000000-0000-4000-8000-000000000001",
    replayed: true,
    title: "Recibo de INK",
    total_milli: 2750,
    transfer_id: "00000000-0000-4000-8000-000000000002",
  });
  assert.equal(replay.replayed, true);
  assert.equal(replay.line, first.line, "a replay replays the original settlement");
});

test("the statement carries only the journal-derived balance", () => {
  const statement: MeteringStatement = {
    balance_milli: 997250,
    entries: [
      {
        posted_at: "2026-10-05T10:01:00Z",
        publication_id: "00000000-0000-4000-8000-000000000001",
        refunded: false,
        service: "argument-publish",
        total_milli: 2750,
      },
    ],
    title: "Extrato de INK",
  };
  const view = statementView(translatorOf("pt-BR"), "pt-BR", statement);

  assert.equal(view.state, "ready");
  if (view.state === "ready") {
    assert.ok(view.balance.includes("997.250") || view.balance.includes("997,250"), `balance misread: ${view.balance}`);
    assert.equal(view.rows.length, 1);
    assert.equal(view.rows[0]?.publicationId, "00000000-0000-4000-8000-000000000001");
  }

  const empty = statementView(translatorOf("en-US"), "en-US", {
    balance_milli: 1000000,
    entries: [],
    title: "INK statement",
  });
  assert.equal(empty.state, "empty");
});

test("amounts refuse unsafe integers instead of rounding a charge", () => {
  assert.throws(() => assertSafeMilli(1.5), TypeError);
  assert.throws(() => assertSafeMilli(Number.MAX_SAFE_INTEGER + 1), TypeError);
  assert.equal(assertSafeMilli(2750), 2750);
});

test("a double click never settles twice", () => {
  const guard = createConfirmGuard();
  assert.equal(guard.tryBegin(), true, "the first press opens the flight");
  assert.equal(guard.tryBegin(), false, "a press inside the flight is refused");
  guard.release();
  assert.equal(guard.tryBegin(), true, "the answer closes the flight");
});

test("metering denials name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(meteringFailure(translator, "unauthorized").includes("Sign in"));
  assert.ok(meteringFailure(translator, "quote_mismatch").includes("another account"));
  assert.ok(meteringFailure(translator, "quote_expired").includes("lapsed"));
  assert.ok(meteringFailure(translator, "price_unavailable").includes("lapsed"));
  assert.ok(meteringFailure(translator, "publish_conflict").includes("different terms"));
  assert.ok(meteringFailure(translator, "publication_unknown").includes("does not exist"));
  assert.equal(
    meteringFailure(translator, "something-the-backend-never-emits"),
    meteringFailure(translator, "unknown-code"),
    "an unknown code must fall back to the generic sentence",
  );
});

test("a disabled capability renders unavailability and guards the confirmation", () => {
  const translator = translatorOf("pt-BR");

  const off = meteringGate(noStagedCapabilities(), translator);
  assert.equal(off.enabled, false);
  assert.ok((off.view?.heading ?? "").includes("indisponível"), "unavailability heading missing");

  const on = meteringGate(stagedCapabilities(["metering"]), translator);
  assert.equal(on.enabled, true);
  assert.equal(on.view, null);

  assert.throws(() => requireMeteringEnabled(noStagedCapabilities()), StagedUnavailableError);
  requireMeteringEnabled(stagedCapabilities(["metering"]));
});
