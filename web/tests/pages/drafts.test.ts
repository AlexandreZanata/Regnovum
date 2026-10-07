/**
 * Tests of the drafts panel presentation (P52-T03).
 *
 * They run the real generated catalogs in both locales: the form
 * carries exactly the contract fields with the bounds as hints, rows
 * render saved drafts verbatim, the single-flight guard refuses a
 * second press while one creation flies, and failures name only the
 * server codes the backend really emits.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  createCreationGuard,
  draftFailure,
  draftFormView,
  draftListView,
  draftRow,
} from "../../src/pages/drafts.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { PrivateArena, PrivateArenaList } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the drafts panel renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["arenas"] });
}

const DRAFT: PrivateArena = {
  id: "018f6b2a-0000-7000-8000-000000000001",
  slug: null,
  statement: "Máquinas podem ser responsáveis?",
  context: "Contexto oferecido pelo autor.",
  category: "philosophy",
  language: "pt-BR",
  status: "draft",
  version: 1,
  created_at: "2026-10-01T10:00:00Z",
  published_at: null,
  closes_at: null,
};

const LIST: PrivateArenaList = { items: [DRAFT] };

test("the form carries exactly the contract fields with hints, never enforcement", () => {
  const view = draftFormView(translatorOf("pt-BR"));

  assert.equal(view.heading, "Meus rascunhos");
  assert.equal(view.submit, "Guardar rascunho");
  assert.deepEqual(view.categories, [
    "technology",
    "science",
    "philosophy",
    "politics",
    "economics",
    "health",
    "culture",
    "society",
  ]);
  assert.deepEqual(view.languages, ["pt-BR", "en-US"]);
  assert.ok(view.statementHint.includes("servidor"), "the bounds are the server's to count");
  const keys = Object.keys(JSON.parse(JSON.stringify(view)));
  for (const marker of ["price", "pass", "ink", "debit", "cost", "amount"]) {
    assert.ok(!keys.includes(marker), `debit field leaked: ${marker}`);
  }
});

test("the form renders in en-US", () => {
  const view = draftFormView(translatorOf("en-US"));

  assert.equal(view.heading, "My drafts");
  assert.equal(view.submit, "Save draft");
});

test("a saved draft renders verbatim with its draft mark", () => {
  const row = draftRow(translatorOf("pt-BR"), "pt-BR", DRAFT);

  assert.equal(row.id, "018f6b2a-0000-7000-8000-000000000001");
  assert.equal(row.statement, "Máquinas podem ser responsáveis?");
  assert.equal(row.category, "philosophy", "the taxonomy is shown, never translated");
  assert.equal(row.version, 1);
  assert.equal(row.status, "Rascunho");
  assert.ok(!row.created.includes("2026-10-01T10:00:00Z"), "raw instant leaked");
});

test("the list shows saved rows and names its empty state", () => {
  const ready = draftListView(translatorOf("en-US"), "en-US", LIST);
  assert.equal(ready.state, "ready");
  if (ready.state === "ready") {
    assert.equal(ready.rows.length, 1);
    assert.equal(ready.rows[0]?.status, "Draft");
  } else {
    assert.ok(false, "a saved list must be ready");
  }

  const empty = draftListView(translatorOf("en-US"), "en-US", { items: [] });
  assert.equal(empty.state, "empty");
  if (empty.state === "empty") {
    assert.equal(empty.empty, "No drafts yet. Write the first controversy.");
  } else {
    assert.ok(false, "an empty list must name its empty state");
  }
});

test("the guard refuses a second press while one creation flies", () => {
  const guard = createCreationGuard();

  assert.equal(guard.tryBegin(), true, "the first press opens the flight");
  assert.equal(guard.tryBegin(), false, "the double press must not become a request");
  assert.equal(guard.tryBegin(), false, "impatient presses keep refused");

  guard.release();
  assert.equal(guard.tryBegin(), true, "the answer closes the flight for the next try");
  guard.release();
});

test("two guards fly independently", () => {
  const first = createCreationGuard();
  const second = createCreationGuard();

  assert.equal(first.tryBegin(), true);
  assert.equal(second.tryBegin(), true, "another form is another flight");
  first.release();
  second.release();
});

test("failures name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  for (const code of [
    "arena_statement_too_short",
    "arena_statement_too_long",
    "arena_context_too_long",
    "arena_invalid_category",
    "arena_invalid_language",
  ]) {
    assert.ok(
      draftFailure(translator, code).includes("did not pass"),
      `unexpected invalid: ${draftFailure(translator, code)}`,
    );
  }
  assert.ok(draftFailure(translator, "rate_limited").includes("Too many requests"));
  assert.equal(
    draftFailure(translator, "draft_too_many"),
    draftFailure(translator, "something-the-backend-never-emits"),
    "an unknown code must fall back to the generic sentence",
  );
});
