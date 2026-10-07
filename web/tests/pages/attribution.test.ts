/**
 * Tests of the influence attribution presentation (P53-T05).
 *
 * They run the real generated catalogs in both locales: the
 * selection keeps only the identifiers the page offered, the public
 * counts render numbers with no identity anywhere, and failures name
 * only the server codes the backend really emits. Nothing here
 * scores anyone: there is no board, no rank and no fabricated
 * total.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  attributionFailure,
  eligibleArguments,
  influenceCountsView,
} from "../../src/pages/attribution.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { ArgumentAttributionMetrics } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespaces the attribution views render from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["arenas"] });
}

const METRICS: ArgumentAttributionMetrics = {
  valid_attributions: 1234,
  distinct_people: 7,
  checked_at: "2026-10-03T10:00:00Z",
};

test("the selection keeps only the identifiers the page offered", () => {
  assert.deepEqual(eligibleArguments(["arg-1", "arg-2"], ["arg-1", "arg-2", "arg-3"]), ["arg-1", "arg-2"]);
  assert.deepEqual(eligibleArguments([], ["arg-1"]), [], "an empty selection is a valid skip");
  assert.deepEqual(
    eligibleArguments(["arg-1", "foreign", "arg-1", "", "arg-2"], ["arg-1", "arg-2"]),
    ["arg-1", "arg-2"],
    "a foreign identifier never becomes a request and duplicates collapse",
  );
  assert.deepEqual(eligibleArguments(["foreign"], ["arg-1"]), []);
});

test("the counts render numbers with no identity anywhere", () => {
  const view = influenceCountsView(translatorOf("pt-BR"), "pt-BR", METRICS);

  assert.equal(view.valid, "Atribuições válidas: 1.234");
  assert.equal(view.people, "Pessoas que creditaram: 7");
  assert.ok(!view.checked.includes("2026-10-03T10:00:00Z"), "raw instant leaked");
  const serialized = JSON.stringify(view).toLowerCase();
  for (const marker of ["email", "account", "handle", "score", "rank", "board"]) {
    assert.ok(!serialized.includes(marker), `identity or scoreboard marker leaked: ${marker}`);
  }

  const en = influenceCountsView(translatorOf("en-US"), "en-US", METRICS);
  assert.equal(en.valid, "Valid attributions: 1,234");
  assert.equal(en.people, "People who credited: 7");
});

test("attribution failures name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(attributionFailure(translator, "persuasion_self_attribution").includes("own argument"));
  assert.ok(attributionFailure(translator, "persuasion_duplicate_attribution").includes("already credited"));
  assert.ok(attributionFailure(translator, "persuasion_cross_arena_argument").includes("another arena"));
  assert.ok(attributionFailure(translator, "persuasion_argument_not_before_change").includes("not eligible"));
  assert.ok(attributionFailure(translator, "persuasion_argument_not_eligible").includes("not eligible"));
  assert.ok(attributionFailure(translator, "persuasion_too_many_attributions").includes("too many"));
  assert.ok(attributionFailure(translator, "change_not_found").includes("no longer available"));
  assert.ok(attributionFailure(translator, "argument_not_found").includes("removed"));
  assert.equal(
    attributionFailure(translator, "persuasion_too_few"),
    attributionFailure(translator, "something-the-backend-never-emits"),
    "an unknown code must fall back to the generic sentence",
  );
});
