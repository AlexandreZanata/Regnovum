/**
 * Tests of the Arena participation page model (P18-T06; position and
 * aggregate journeys P53-T01).
 *
 * The page is server-rendered and complete without any script, so what is proven
 * here is what the module adds on top: the local choice, which must never become
 * a value the page did not render, the attribution selection, which must
 * never pass the bound the server declares, and the translated projection
 * of the position journey — confirmation and change forms, the owner's
 * private head and history, and failures naming only the server codes the
 * backend really emits. The element wiring is not executed — it cannot be,
 * without a browser — and that is the reason every decision lives in this
 * DOM-free model.
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
  myPositionView,
  positionChangeView,
  positionChoices,
  positionConfirmView,
  positionFailure,
  positionFailureField,
  positionHistoryHeading,
  positionHistoryView,
  positionLabel,
  readLocalChoice,
} from "../../src/pages/participation.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { PositionChangeHistory, PrivatePosition } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the participation page renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["arenas"] });
}

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

const MINE: PrivatePosition = {
  arena_id: "018f6b2a-0000-7000-8000-000000000001",
  initial_position: "agree",
  current_position: "disagree",
  version: 2,
  created_at: "2026-10-02T10:00:00Z",
  updated_at: "2026-10-02T12:00:00Z",
};

const HISTORY: PositionChangeHistory = {
  items: [
    {
      change_id: "change-2",
      from_position: "disagree",
      to_position: "undecided",
      version: 3,
      changed_at: "2026-10-02T13:00:00Z",
    },
    {
      change_id: "change-1",
      from_position: "agree",
      to_position: "disagree",
      version: 2,
      changed_at: "2026-10-02T12:00:00Z",
    },
  ],
};

test("the vocabulary labels translate in both locales", () => {
  assert.equal(positionLabel(translatorOf("pt-BR"), "agree"), "A favor");
  assert.equal(positionLabel(translatorOf("pt-BR"), "disagree"), "Contra");
  assert.equal(positionLabel(translatorOf("pt-BR"), "undecided"), "Sem posição");
  assert.equal(positionLabel(translatorOf("en-US"), "agree"), "Agree");

  assert.deepEqual(
    positionChoices(translatorOf("en-US")).map((choice) => choice.value),
    ["agree", "disagree", "undecided"],
  );
});

test("the confirmation starts from the visitor's local choice and nothing else", () => {
  const view = positionConfirmView(translatorOf("pt-BR"), "disagree");

  assert.equal(view.heading, "Confirmar posição inicial");
  assert.ok(view.intro.includes("imutável"), `unexpected intro: ${view.intro}`);
  assert.equal(view.suggested, "disagree");
  assert.equal(view.submit, "Confirmar posição");
  assert.equal(view.choices.length, 3);

  const fresh = positionConfirmView(translatorOf("en-US"), null);
  assert.equal(fresh.heading, "Confirm initial position");
  assert.equal(fresh.suggested, null, "no stored choice means no suggestion");
  assert.equal(fresh.submit, "Confirm position");
});

test("the change names the current position the move leaves", () => {
  const view = positionChangeView(translatorOf("pt-BR"), "disagree");

  assert.equal(view.heading, "Mudar posição");
  assert.ok(view.current.includes("Contra"), `unexpected current: ${view.current}`);
  assert.equal(view.submit, "Mudar posição");

  const en = positionChangeView(translatorOf("en-US"), "agree");
  assert.ok(en.current.includes("Agree"));
});

test("the owner's head renders both sides with its version", () => {
  const view = myPositionView(translatorOf("pt-BR"), "pt-BR", MINE);

  assert.ok(view.initial.includes("A favor"), `unexpected initial: ${view.initial}`);
  assert.ok(view.current.includes("Contra"), `unexpected current: ${view.current}`);
  assert.equal(view.version, 2);
  assert.ok(!view.updated.includes("2026-10-02T12:00:00Z"), "raw instant leaked");

  const en = myPositionView(translatorOf("en-US"), "en-US", MINE);
  assert.ok(en.initial.includes("Agree"));
  assert.ok(en.current.includes("Disagree"));
});

test("the history renders newest first with translated moves", () => {
  assert.equal(positionHistoryHeading(translatorOf("pt-BR")), "Histórico de posições");

  const view = positionHistoryView(translatorOf("pt-BR"), "pt-BR", HISTORY);
  assert.equal(view.state, "ready");
  if (view.state === "ready") {
    assert.equal(view.rows.length, 2);
    assert.deepEqual(
      view.rows.map((row) => [row.from, row.to]),
      [
        ["Contra", "Sem posição"],
        ["A favor", "Contra"],
      ],
    );
    assert.deepEqual(
      view.rows.map((row) => row.version),
      [3, 2],
    );
    assert.ok(!view.rows[0]?.changed.includes("2026-10-02T13:00:00Z"), "raw instant leaked");
  } else {
    assert.ok(false, "a recorded history must be ready");
  }
});

test("a history that has not started names its empty state", () => {
  const view = positionHistoryView(translatorOf("en-US"), "en-US", { items: [] });

  assert.equal(view.state, "empty");
  if (view.state === "empty") {
    assert.equal(view.empty, "No changes recorded. The initial confirmation starts the history.");
  } else {
    assert.ok(false, "an empty history must name its empty state");
  }
});

test("position failures name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(positionFailure(translator, "position_invalid").includes("listed positions"));
  assert.ok(positionFailure(translator, "initial_position_already_set").includes("already confirmed"));
  assert.ok(positionFailure(translator, "version_conflict").includes("changed elsewhere"));
  assert.ok(positionFailure(translator, "position_not_found").includes("not confirmed"));
  assert.ok(positionFailure(translator, "arena_not_open").includes("no more positions"));
  assert.ok(positionFailure(translator, "account_not_eligible").includes("cannot participate"));
  assert.ok(positionFailure(translator, "account_suspended").includes("suspended"));
  assert.ok(positionFailure(translator, "position_same").includes("different from the current"));
  assert.ok(positionFailure(translator, "arena_not_found").includes("not been published"));
  assert.equal(
    positionFailure(translator, "position_too_many"),
    positionFailure(translator, "something-the-backend-never-emits"),
    "an unknown code must fall back to the generic sentence",
  );
});

test("position failures focus the position field only for a bad value", () => {
  assert.equal(positionFailureField("position_invalid"), "position");
  assert.equal(positionFailureField("initial_position_already_set"), null);
  assert.equal(positionFailureField("version_conflict"), null);
  assert.equal(positionFailureField("position_not_found"), null);
  assert.equal(positionFailureField("something-the-backend-never-emits"), null);
});
