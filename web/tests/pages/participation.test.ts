/**
 * Tests of the Arena participation page model (P18-T06).
 *
 * The page is server-rendered and complete without any script, so what is proven
 * here is what the module adds on top: the local choice, which must never become
 * a value the page did not render, and the attribution selection, which must
 * never pass the bound the server declares. The element wiring is not executed —
 * it cannot be, without a browser — and that is the reason every decision lives
 * in this DOM-free model.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  POSITIONS,
  arenaSlugFromPath,
  attributionLimitMessage,
  attributionSelection,
  choiceStorageKey,
  chooseLocalChoice,
  readLocalChoice,
} from "../../src/pages/participation.js";
import { createTranslator } from "../../src/i18n/translator.js";

test("the position vocabulary is the one the generated contract declares", () => {
  assert.deepEqual([...POSITIONS], ["agree", "disagree", "undecided"]);
});

test("the storage key is scoped to one Arena slug", () => {
  const first = choiceStorageKey("arena-a");
  const second = choiceStorageKey("arena-b");

  assert.notEqual(first, second);
  assert.ok(first.endsWith("arena-a"), `expected the key to end with the Arena slug, got ${first}`);
  assert.ok(!first.includes("arena-b"), "one Arena must never read the choice of another");
});

test("the slug is read from the participation pathname", () => {
  assert.equal(arenaSlugFromPath("/arenas/first-arena"), "first-arena");
  assert.equal(arenaSlugFromPath("/arenas/first-arena/"), "first-arena");
  assert.equal(arenaSlugFromPath("//arenas//first-arena//"), "first-arena");
});

test("an address that is not a participation page has no slug", () => {
  for (const pathname of ["", "/", "/arenas", "/arenas/", "/d/first-arena", "/arenas/a/b", "/outra/first-arena"]) {
    assert.equal(arenaSlugFromPath(pathname), null, `arenaSlugFromPath(${JSON.stringify(pathname)}) must refuse`);
  }
});

test("a stored value is accepted only when it is a position of the page", () => {
  assert.equal(readLocalChoice("agree"), "agree");
  assert.equal(readLocalChoice(" disagree "), "disagree");
  assert.equal(readLocalChoice("undecided"), "undecided");
});

test("no stored value, or one outside the vocabulary, is no choice at all", () => {
  for (const stored of [null, "", "   ", "talvez", "AGREE", "0", "null", "<script>", "agree;disagree"]) {
    assert.equal(readLocalChoice(stored), null, `readLocalChoice(${JSON.stringify(stored)}) must refuse`);
  }
});

test("a click on a button outside the vocabulary stores nothing", () => {
  const refused = chooseLocalChoice("talvez");
  assert.equal(refused.accepted, false);
  assert.equal(refused.position, null);

  const accepted = chooseLocalChoice("disagree");
  assert.equal(accepted.accepted, true);
  assert.equal(accepted.position, "disagree");
});

test("a selection within the limit is accepted", () => {
  const first = attributionSelection([], "argument-1", true, 3);
  assert.deepEqual(first, { allowed: true, selected: ["argument-1"] });

  const second = attributionSelection(first.selected, "argument-2", true, 3);
  assert.deepEqual(second, { allowed: true, selected: ["argument-1", "argument-2"] });
});

test("a selection that would pass the limit is refused, and the refusal keeps the previous one", () => {
  const full = ["argument-1", "argument-2", "argument-3"];
  const decision = attributionSelection(full, "argument-4", true, 3);

  assert.equal(decision.allowed, false);
  assert.deepEqual(decision.selected, full, "a refused toggle must not change the selection");
});

test("a limit the page did not declare refuses every tick", () => {
  for (const limit of [0, -1, Number.NaN]) {
    const decision = attributionSelection([], "argument-1", true, limit);
    assert.equal(decision.allowed, false, `limit ${String(limit)} must refuse the tick`);
    assert.deepEqual(decision.selected, []);
  }
});

test("unchecking an argument is always allowed and never depends on the limit", () => {
  const selection = ["argument-1", "argument-2", "argument-3"];
  const decision = attributionSelection(selection, "argument-2", false, 3);

  assert.equal(decision.allowed, true);
  assert.deepEqual(decision.selected, ["argument-1", "argument-3"]);

  const unbounded = attributionSelection(selection, "argument-1", false, 0);
  assert.equal(unbounded.allowed, true);
  assert.deepEqual(unbounded.selected, ["argument-2", "argument-3"]);
});

test("toggling the same argument twice is idempotent", () => {
  const once = attributionSelection([], "argument-1", true, 3);
  const twice = attributionSelection(once.selected, "argument-1", true, 3);

  assert.deepEqual(twice.selected, once.selected);
  assert.equal(twice.allowed, true);
});

test("an empty value is never a selection", () => {
  const decision = attributionSelection([], "", true, 3);
  assert.equal(decision.allowed, false);
  assert.deepEqual(decision.selected, []);
});

test("the refusal message names the limit in the locale of the page", () => {
  const pt = createTranslator("pt-BR", { namespaces: ["arenas"] });
  const en = createTranslator("en-US", { namespaces: ["arenas"] });

  assert.equal(attributionLimitMessage(pt, 3), "Escolha no máximo 3 argumentos.");
  assert.equal(attributionLimitMessage(en, 3), "Choose at most 3 arguments.");
});

test("the refusal message formats a large limit for the locale", () => {
  const pt = createTranslator("pt-BR", { namespaces: ["arenas"] });
  const en = createTranslator("en-US", { namespaces: ["arenas"] });

  assert.ok(attributionLimitMessage(pt, 1234).includes("1.234"));
  assert.ok(attributionLimitMessage(en, 1234).includes("1,234"));
});
