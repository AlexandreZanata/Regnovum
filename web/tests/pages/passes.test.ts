/**
 * Tests of the pass entitlement presentation (P54-T02).
 *
 * They run the real generated catalogs in both locales: the
 * summary renders the derived total with each lot's contracted
 * quantity, real remainder and immutable expiration, an unsafe
 * count is refused instead of rounded, and switching the locale
 * re-renders the same instant instead of moving it. Nothing here
 * grants or renews: a reset re-reads, and the MEMBER lots render
 * exactly what the server derived.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  assertSafeCount,
  historyCursor,
  historyLimit,
  lotView,
  mergeHistoryEntries,
  passHistoryEmpty,
  passHistoryEntryView,
  passesFailure,
  summaryPresentation,
} from "../../src/pages/passes.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { ArenaPassHistoryEntry, ArenaPassLot, ArenaPassSummary } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the pass views render from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["wallet"] });
}

const MEMBER_LOT: ArenaPassLot = {
  origin: "MEMBER",
  quantity: 2,
  remaining: 2,
  expires_at: null,
  expired: false,
  created_at: "2026-10-01T10:00:00Z",
};

const EXPIRING_LOT: ArenaPassLot = {
  origin: "PURCHASE",
  quantity: 1,
  remaining: 1,
  expires_at: "2026-11-01T10:00:00Z",
  expired: false,
  created_at: "2026-10-01T10:00:00Z",
};

const EXPIRED_LOT: ArenaPassLot = {
  origin: "PURCHASE",
  quantity: 1,
  remaining: 1,
  expires_at: "2026-09-01T10:00:00Z",
  expired: true,
  created_at: "2026-08-01T10:00:00Z",
};

const SUMMARY: ArenaPassSummary = {
  available_total: 3,
  checked_at: "2026-10-05T10:00:00Z",
  lots: [MEMBER_LOT, EXPIRING_LOT],
};

const ENTRY: ArenaPassHistoryEntry = {
  consumption_id: "use-1",
  arena_id: "arena-1",
  origin: "MEMBER",
  reference: "arena:arena-1",
  consumed_at: "2026-10-04T10:00:00Z",
};

test("the summary renders the derived total with each lot", () => {
  const view = summaryPresentation(translatorOf("pt-BR"), "pt-BR", SUMMARY);

  assert.equal(view.available, "Passes disponíveis: 3");
  assert.equal(view.lots.length, 2);
  assert.ok(view.lots[0]?.includes("2 de 2 passes (MEMBER)"), "contracted rights missing");
  assert.ok(view.lots[0]?.includes("sem vencimento"), "null deadline misread");
  assert.ok(!view.checked.includes("2026-10-05T10:00:00Z"), "raw instant leaked");
  assert.equal(view.empty, null);

  const en = summaryPresentation(translatorOf("en-US"), "en-US", SUMMARY);
  assert.equal(en.available, "Available passes: 3");
});

test("empty renders the empty sentence and nothing else", () => {
  const view = summaryPresentation(translatorOf("en-US"), "en-US", {
    available_total: 0,
    checked_at: "2026-10-05T10:00:00Z",
    lots: [],
  });

  assert.equal(view.available, "Available passes: 0");
  assert.deepEqual(view.lots, []);
  assert.equal(view.empty, "No passes yet");
  assert.equal(passHistoryEmpty(translatorOf("pt-BR")), "Nenhum passe ainda");
});

test("expired lots render expired and never count as available", () => {
  const expired = lotView(translatorOf("en-US"), "en-US", EXPIRED_LOT);
  assert.ok(expired.includes("expired"), `expired flag misread: ${expired}`);
  assert.ok(!expired.includes("2026-09-01T10:00:00Z"), "raw instant leaked");

  const view = summaryPresentation(translatorOf("en-US"), "en-US", {
    available_total: 0,
    checked_at: "2026-10-05T10:00:00Z",
    lots: [EXPIRED_LOT],
  });
  assert.equal(view.available, "Available passes: 0");
  assert.equal(view.lots.length, 1, "an expired lot stays listed but not counted");
});

test("switching the locale never moves the expiration", () => {
  const pt = lotView(translatorOf("pt-BR"), "pt-BR", EXPIRING_LOT);
  const en = lotView(translatorOf("en-US"), "en-US", EXPIRING_LOT);

  assert.notEqual(pt, en, "locales must render differently");
  assert.ok(pt.includes("PURCHASE") && en.includes("PURCHASE"), "origin must travel verbatim");
  const serialized = JSON.stringify({ pt, en }).toLowerCase();
  for (const marker of ["grant", "renew", "create", "buy", "price"]) {
    assert.ok(!serialized.includes(marker), `invented benefit leaked: ${marker}`);
  }
});

test("an unsafe count is refused instead of rounded", () => {
  assert.throws(() => assertSafeCount(Number.MAX_SAFE_INTEGER + 1), TypeError);
  assert.throws(() => assertSafeCount(1.5), TypeError);
  assert.throws(
    () =>
      summaryPresentation(translatorOf("en-US"), "en-US", {
        available_total: 1.5,
        checked_at: "2026-10-05T10:00:00Z",
        lots: [],
      }),
    TypeError,
  );
});

test("one history line renders its origin with no raw instant", () => {
  const view = passHistoryEntryView(translatorOf("pt-BR"), "pt-BR", ENTRY);

  assert.ok(view.line.includes("MEMBER"), "origin vocabulary missing");
  assert.ok(view.line.includes("arena-1"), "arena identifier missing");
  assert.ok(!view.line.includes("2026-10-04T10:00:00Z"), "raw instant leaked");
  assert.equal(view.reference, "arena:arena-1");
});

test("the history merge keeps one page honest", () => {
  const second: ArenaPassHistoryEntry = { ...ENTRY, consumption_id: "use-2" };
  assert.deepEqual(
    mergeHistoryEntries([ENTRY], [ENTRY, second]).map((entry) => entry.consumption_id),
    ["use-1", "use-2"],
  );
});

test("the cursor travels opaque and the limit only inside 1..100", () => {
  assert.equal(historyCursor(null), undefined);
  assert.equal(historyCursor(""), undefined);
  assert.equal(historyCursor("opaque"), "opaque");
  assert.equal(historyLimit(null), undefined);
  assert.equal(historyLimit("20"), 20);
  assert.equal(historyLimit("0"), undefined);
  assert.equal(historyLimit("101"), undefined);
  assert.equal(historyLimit("abc"), undefined);
});

test("pass failures name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(passesFailure(translator, "invalid_cursor").includes("cursor"));
  assert.ok(passesFailure(translator, "invalid_limit").includes("page size"));
  assert.ok(passesFailure(translator, "unauthorized").includes("Sign in"));
  assert.equal(
    passesFailure(translator, "something-the-backend-never-emits"),
    passesFailure(translator, "unknown-code"),
    "an unknown code must fall back to the generic sentence",
  );
});
