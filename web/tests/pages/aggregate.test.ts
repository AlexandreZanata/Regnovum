/**
 * Tests of the aggregate translation of the Arena page (P18-T06 journey).
 *
 * They run the real generated catalogs in both locales, so a key that moves, a
 * placeholder that changes or a locale that stops grouping numbers fails here
 * instead of on a page a person reads.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { aggregatePresentation } from "../../src/pages/aggregate.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { PositionAggregate } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespaces the aggregate renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["arenas"] });
}

/** A published aggregate with a total large enough to be grouped. */
const PUBLISHED: PositionAggregate = {
  checked_at: "2026-09-23T13:00:00Z",
  current: { agree: 10, disagree: 5, undecided: 2 },
  initial: { agree: 8, disagree: 6, undecided: 3 },
  participants_total: 1234,
  suppressed: false,
};

/** The same Arena below the privacy threshold. */
const SUPPRESSED: PositionAggregate = {
  checked_at: "2026-09-23T13:00:00Z",
  current: { agree: 1, disagree: 1, undecided: 0 },
  initial: { agree: 1, disagree: 1, undecided: 0 },
  participants_total: 2,
  suppressed: true,
};

test("the aggregate renders in pt-BR with the locale's grouping and labels", () => {
  const view = aggregatePresentation(translatorOf("pt-BR"), "pt-BR", PUBLISHED);

  assert.equal(view.heading, "Resultado agregado");
  assert.equal(view.total, "Participantes elegíveis: 1.234");
  assert.equal(view.currentHeading, "Posição atual");
  assert.equal(view.initialHeading, "Posição inicial");
  assert.deepEqual(
    view.current.map((row) => row.label),
    ["A favor", "Contra", "Sem posição"],
  );
  assert.deepEqual(
    view.current.map((row) => row.count),
    ["10", "5", "2"],
  );
  assert.ok(view.checked.startsWith("Apurado em "), `unexpected checked text: ${view.checked}`);
  assert.ok(!view.checked.includes(PUBLISHED.checked_at), "the instant must be formatted, never raw");
});

test("the aggregate renders in en-US with the locale's grouping and labels", () => {
  const view = aggregatePresentation(translatorOf("en-US"), "en-US", PUBLISHED);

  assert.equal(view.heading, "Aggregate result");
  assert.equal(view.total, "Eligible participants: 1,234");
  assert.equal(view.currentHeading, "Current position");
  assert.equal(view.initialHeading, "Initial position");
  assert.deepEqual(
    view.current.map((row) => row.label),
    ["Agree", "Disagree", "Undecided"],
  );
  assert.ok(view.checked.startsWith("Derived at "), `unexpected checked text: ${view.checked}`);
  assert.ok(!view.checked.includes(PUBLISHED.checked_at), "the instant must be formatted, never raw");
});

test("a suppressed aggregate never renders a count in either locale", () => {
  for (const locale of ["pt-BR", "en-US"] as const) {
    const view = aggregatePresentation(translatorOf(locale), locale, SUPPRESSED);

    assert.equal(view.total, null, locale);
    assert.equal(view.currentHeading, null, locale);
    assert.equal(view.initialHeading, null, locale);
    assert.deepEqual(view.current, [], locale);
    assert.deepEqual(view.initial, [], locale);
    assert.notEqual(view.suppressedNote, null, locale);
    assert.ok(!view.checked.includes(SUPPRESSED.checked_at), `the instant must be formatted in ${locale}`);
  }
});

test("the row counts of both locales are the same numbers, formatted per locale", () => {
  const pt = aggregatePresentation(translatorOf("pt-BR"), "pt-BR", PUBLISHED);
  const en = aggregatePresentation(translatorOf("en-US"), "en-US", PUBLISHED);

  assert.deepEqual(
    pt.current.map((row) => row.count),
    en.current.map((row) => row.count),
  );
});

test("an instant the contract cannot have is refused instead of rendered", () => {
  const broken: PositionAggregate = { ...PUBLISHED, checked_at: "not-an-instant" };

  assert.throws(() => aggregatePresentation(translatorOf("pt-BR"), "pt-BR", broken), RangeError);
});
