/**
 * Tests of the argument publication presentation (P53-T03).
 *
 * They run the real generated catalogs in both locales: the form
 * carries relation and content with the server ceiling as a hint,
 * the count is feedback the author watches while typing, a refusal
 * keeps exactly the typed text, the second press of a double click
 * never becomes a request, and failures name only the server codes
 * the backend really emits. No sentence prices anything: the debit
 * belongs to the server, and the balance surface belongs to the
 * wallet phase.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  MAX_GRAPHEMES,
  contentFeedback,
  countGraphemes,
  publishFailure,
  publishFailureField,
  publishFormView,
} from "../../src/pages/publication.js";
import { submissionStart } from "../../src/pages/submission.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespaces the publication form renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["arenas"] });
}

test("the ceiling is the contract's, in clusters", () => {
  assert.equal(MAX_GRAPHEMES, 3000);
});

test("the count follows grapheme clusters, not code units", () => {
  assert.equal(countGraphemes("verificação"), 11);
  assert.equal(countGraphemes("👨‍👩‍👧‍👦"), 1);
  assert.equal(countGraphemes("é"), 1);
  assert.equal(countGraphemes(""), 0);
});

test("the feedback warns past the ceiling without ever blocking", () => {
  const short = contentFeedback(translatorOf("pt-BR"), "pt-BR", "Um argumento curto.");
  assert.equal(short.tooLong, false);
  assert.equal(short.remaining, MAX_GRAPHEMES - short.clusters);
  assert.ok(short.count.includes("3.000"), `unexpected: ${short.count}`);

  const long = contentFeedback(translatorOf("en-US"), "en-US", "x".repeat(3001));
  assert.equal(long.clusters, 3001);
  assert.equal(long.tooLong, true);
  assert.equal(long.remaining, -1);
  assert.ok(long.count.includes("3,000"));
});

test("the form carries relation and content with the server hint", () => {
  const view = publishFormView(translatorOf("pt-BR"));

  assert.equal(view.heading, "Publicar argumento");
  assert.ok(view.intro.includes("INK"), `the debit fact travels: ${view.intro}`);
  assert.deepEqual(
    view.relations.map((choice) => choice.value),
    ["support", "oppose", "context"],
  );
  assert.deepEqual(
    view.relations.map((choice) => choice.label),
    ["A favor", "Contra", "Contexto"],
  );
  assert.ok(view.contentHint.includes("3.000"), `unexpected: ${view.contentHint}`);
  assert.deepEqual(view.unsent, { relation: null, content: "" });
  assert.equal(view.submit, "Publicar");

  const en = publishFormView(translatorOf("en-US"));
  assert.equal(en.heading, "Publish an argument");
  assert.equal(en.submit, "Publish");
});

test("a refusal keeps exactly the typed text", () => {
  const unsent = { relation: "support", content: "Texto digitado que não pode se perder." };
  const view = publishFormView(translatorOf("pt-BR"), unsent);

  assert.deepEqual(view.unsent, unsent);
  assert.equal(view.unsent.content, "Texto digitado que não pode se perder.");
});

test("the second press of a double click never becomes a request", () => {
  assert.equal(submissionStart(false).allow, true, "the first press always goes");
  assert.equal(submissionStart(true).allow, false, "the impatient press must not debit twice");
});

test("the form prices nothing anywhere", () => {
  const serialized = JSON.stringify(publishFormView(translatorOf("en-US"))).toLowerCase();
  // The intro states the server-side debit fact ("balance" in the catalog
  // sentence); what must never appear is anything the browser computed.
  for (const marker of ["price", "amount", "total", "preview", "wallet"]) {
    assert.ok(!serialized.includes(marker), `monetary marker leaked: ${marker}`);
  }
});

test("publication failures name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(publishFailure(translator, "insufficient_ink").includes("balance"));
  assert.ok(publishFailure(translator, "argument_empty_content").includes("value for this field"));
  assert.ok(publishFailure(translator, "argument_invalid_content").includes("3,000 graphemes"));
  assert.ok(publishFailure(translator, "argument_content_too_long").includes("3,000 graphemes"));
  assert.ok(publishFailure(translator, "argument_empty_relation").includes("listed relations"));
  assert.ok(publishFailure(translator, "argument_invalid_relation").includes("listed relations"));
  assert.ok(publishFailure(translator, "argument_invalid_source_url").includes("http(s)"));
  assert.ok(publishFailure(translator, "argument_source_description_too_long").includes("http(s)"));
  assert.ok(publishFailure(translator, "arena_not_open").includes("no more positions"));
  assert.ok(publishFailure(translator, "account_not_eligible").includes("cannot participate"));
  assert.ok(publishFailure(translator, "account_suspended").includes("suspended"));
  assert.ok(publishFailure(translator, "arena_not_found").includes("not been published"));
  assert.equal(
    publishFailure(translator, "argument_too_many"),
    publishFailure(translator, "something-the-backend-never-emits"),
    "an unknown code must fall back to the generic sentence",
  );
});

test("publication failures focus relation or content, nothing else", () => {
  assert.equal(publishFailureField("argument_empty_relation"), "relation");
  assert.equal(publishFailureField("argument_invalid_relation"), "relation");
  assert.equal(publishFailureField("argument_empty_content"), "content");
  assert.equal(publishFailureField("argument_content_too_long"), "content");
  assert.equal(publishFailureField("insufficient_ink"), "content");
  assert.equal(publishFailureField("argument_invalid_source_url"), null);
  assert.equal(publishFailureField("arena_not_open"), null);
  assert.equal(publishFailureField("something-the-backend-never-emits"), null);
});
