/**
 * Tests of the public transparency document (P55-T04).
 *
 * They run the real generated catalogs in both locales: the
 * document renders methodology, period and localized rows for
 * exactly the codes the server sent — a missing code renders
 * no row, never an inferred zero — and the canonical link
 * carries only what the contract declares. No empty report is
 * ever fabricated.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  METRIC_CODES,
  assertSafeCount,
  documentLink,
  documentPresentation,
} from "../../src/pages/transparency.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { TransparencyMetrics } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the document renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["transparency"] });
}

const COUNTS = {
  eligible_accounts: 120,
  arenas_published: 9,
  arenas_closed: 1,
  arenas_restricted: 0,
  arenas_removed: 0,
  arguments_published: 40,
  arguments_withdrawn: 2,
  position_changes: 15,
  attributions_valid: 30,
  attributions_invalidated: 1,
  influenced_authors: 11,
  ink_free_granted: 100000,
  ink_free_expired: 5000,
  ink_free_consumed: 20000,
  ink_purchased_granted: 50000,
  ink_purchased_consumed: 8000,
  ink_refunded: 0,
  ink_admin_adjusted: 0,
  passes_purchase_granted: 12,
  passes_member_granted: 3,
  passes_consumed: 9,
  reports_filed: 4,
  actions_recorded: 1,
  appeals_filed: 1,
  appeals_reversed: 0,
} as const;

const DOCUMENT: TransparencyMetrics = {
  methodology_version: 2,
  period_start: "2026-09-05T00:00:00Z",
  period_end: "2026-10-05T00:00:00Z",
  timezone: "UTC",
  updated_at: "2026-10-05T10:00:00Z",
  metrics: { ...COUNTS },
};

test("the document renders every sent code under a localized label", () => {
  assert.equal(METRIC_CODES.length, 25);

  const view = documentPresentation(translatorOf("pt-BR"), "pt-BR", DOCUMENT);

  assert.equal(view.rows.length, 25);
  assert.ok(view.period.includes("UTC"), "timezone echo missing");
  assert.ok(view.updated.includes("2"), "methodology version missing");
  assert.ok(!view.period.includes("2026-09-05T00:00:00Z"), "raw instant leaked");
  const reports = view.rows.find((row) => row.metric === "Denúncias registradas");
  assert.equal(reports?.value, "4");

  const en = documentPresentation(translatorOf("en-US"), "en-US", DOCUMENT);
  assert.ok(en.rows.some((row) => row.metric === "Filed reports" && row.value === "4"));
});

test("a missing code renders no row and no empty report is fabricated", () => {
  const { appeals_reversed: _omitted, ...partial } = COUNTS;
  const partialMetrics = { ...partial } as unknown as TransparencyMetrics["metrics"];
  const view = documentPresentation(translatorOf("en-US"), "en-US", {
    ...DOCUMENT,
    metrics: partialMetrics,
  });

  assert.equal(view.rows.length, 24);
  assert.ok(!view.rows.some((row) => row.metric === "Reversed appeals"), "an inferred row leaked");
});

test("an unsafe count is refused instead of rounded", () => {
  assert.throws(() => assertSafeCount(Number.MAX_SAFE_INTEGER + 1), TypeError);
  assert.throws(() => assertSafeCount(1.5), TypeError);
});

test("the canonical link carries only what the contract declares", () => {
  assert.equal(documentLink(), "/transparency");
  assert.equal(
    documentLink({ period_start: "2026-09-05T00:00:00Z", locale: "pt-BR" }),
    "/transparency?period_start=2026-09-05T00%3A00%3A00Z&locale=pt-BR",
  );
  assert.equal(documentLink({ period_start: "not-an-instant", locale: "xx" }), "/transparency");
  assert.equal(documentLink({ timezone: "America/Sao_Paulo" }), "/transparency?timezone=America%2FSao_Paulo");
});
