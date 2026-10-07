/**
 * Tests of the argument reading and discovery presentation (P53-T02).
 *
 * They run the real generated catalogs in both locales: rows render
 * user content verbatim with translated relations, a withdrawn read
 * keeps identity and status while withholding content, the search
 * carries the query verbatim without changing any Arena language,
 * pages merge without duplication while late answers are discarded,
 * sources pass a structural check that refuses the malicious, and
 * failures name only the server codes the backend really emits.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  argumentFailure,
  argumentListView,
  argumentRow,
  argumentSearchListView,
  argumentSearchRow,
  argumentView,
  isSafeSourceUrl,
  isStaleArgumentResponse,
  mergeArgumentPage,
  mergeArgumentSearchPage,
  parseArgumentSearch,
  relationLabel,
  serializeArgumentSearch,
} from "../../src/pages/arguments.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type {
  Argument,
  ArgumentListItem,
  SearchArgumentResult,
} from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespaces the arguments page renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["arenas"] });
}

const ITEM: ArgumentListItem = {
  arena_id: "arena-1",
  content: "Because responsibility presupposes consciousness.",
  created_at: "2026-10-02T11:00:00Z",
  id: "arg-1",
  parent_id: null,
  relation: "support",
  reply_count: 2,
  status: "published",
};

const RESULT: SearchArgumentResult = {
  arena_id: "arena-1",
  content: "Porque a responsabilidade pressupõe consciência.",
  created_at: "2026-10-02T11:00:00Z",
  id: "arg-2",
  language: "pt-BR",
  relation: "support",
  score: 0.9,
};

test("relations translate while the code never renders", () => {
  assert.equal(relationLabel(translatorOf("pt-BR"), "support"), "A favor");
  assert.equal(relationLabel(translatorOf("pt-BR"), "oppose"), "Contra");
  assert.equal(relationLabel(translatorOf("pt-BR"), "context"), "Contexto");
  assert.equal(relationLabel(translatorOf("en-US"), "support"), "Support");
});

test("a listed row renders content verbatim with its reply count", () => {
  const row = argumentRow(translatorOf("pt-BR"), "pt-BR", ITEM);

  assert.equal(row.id, "arg-1");
  assert.equal(row.arenaId, "arena-1");
  assert.equal(row.relation, "A favor");
  assert.equal(row.content, "Because responsibility presupposes consciousness.");
  assert.equal(row.replies, 2);
  assert.equal(row.status, "published");
  assert.ok(!row.created.includes("2026-10-02T11:00:00Z"), "raw instant leaked");
});

test("a withdrawn read keeps identity and status while withholding content", () => {
  const withdrawn: Argument = { ...ITEM, content: null, status: "withdrawn" };
  const view = argumentView(translatorOf("pt-BR"), "pt-BR", withdrawn);

  assert.equal(view.id, "arg-1");
  assert.equal(view.status, "withdrawn");
  assert.equal(view.content, null, "withdrawn content is never reconstructed");
  assert.ok((view.withdrawnNote ?? "").includes("retirado"), `unexpected: ${view.withdrawnNote}`);

  const published: Argument = { ...ITEM, status: "published" };
  assert.equal(argumentView(translatorOf("en-US"), "en-US", published).withdrawnNote, null);
  assert.ok(
    (argumentView(translatorOf("en-US"), "en-US", withdrawn).withdrawnNote ?? "").includes("withdrawn"),
  );
});

test("a blank search is a hint, never a fetch", () => {
  for (const search of ["", "?q=", "?q=++", "?language=pt-BR"]) {
    assert.equal(parseArgumentSearch(search), null, `parseArgumentSearch(${search}) must be a hint`);
  }
});

test("a search travels verbatim with the allowlisted filters", () => {
  const query = parseArgumentSearch("?q=consciência&language=pt-BR&limit=20&cursor=c1&score=9&arena=x");

  assert.deepEqual(query, { q: "consciência", language: "pt-BR", limit: 20, cursor: "c1" });
  assert.equal(
    serializeArgumentSearch({ q: "consciência", language: "pt-BR", limit: 20, cursor: "c1" }),
    "q=consci%C3%AAncia&language=pt-BR&cursor=c1&limit=20",
  );
});

test("a search outside the vocabulary is dropped, never sent to fail", () => {
  assert.deepEqual(parseArgumentSearch("?q=minds&language=fr&limit=500"), { q: "minds" });
  assert.deepEqual(parseArgumentSearch("?q=minds&limit=0"), { q: "minds" });
});

test("a result keeps its own language: the search changes none", () => {
  const row = argumentSearchRow(translatorOf("en-US"), "en-US", RESULT);

  assert.equal(row.language, "pt-BR", "the result language renders as-is");
  assert.equal(row.content, "Porque a responsabilidade pressupõe consciência.");
  assert.equal(row.relation, "Support");
  const serialized = JSON.stringify(row);
  assert.ok(!serialized.includes("score"), "the ordering signal stays out of the view");
});

test("pages merge without duplication and the cursor extends", () => {
  const translator = translatorOf("en-US");
  const first = mergeArgumentPage(translator, "en-US", [], { items: [ITEM], next_cursor: "c1" });
  const second = mergeArgumentPage(translator, "en-US", first.rows, {
    items: [ITEM, { ...ITEM, id: "arg-2" }],
    next_cursor: null,
  });

  assert.deepEqual(
    second.rows.map((row) => row.id),
    ["arg-1", "arg-2"],
  );
  assert.equal(second.nextCursor, null);

  const list = argumentListView(translator, second);
  assert.equal(list.state, "ready");
  if (list.state === "ready") {
    assert.equal(list.more, null);
    assert.equal(list.end, "No more results.");
  }

  const empty = argumentListView(translatorOf("pt-BR"), { rows: [], nextCursor: null });
  assert.equal(empty.state, "empty");
  if (empty.state === "empty") {
    assert.equal(empty.empty, "Ainda não há argumento publicado nesta relação.");
  }
});

test("a search list names its hint and its empty state", () => {
  const translator = translatorOf("en-US");

  const hint = argumentSearchListView(translator, null, { rows: [], nextCursor: null });
  assert.equal(hint.state, "hint");
  if (hint.state === "hint") {
    assert.equal(hint.hint, "Type what you seek in the published arguments.");
  }

  const empty = argumentSearchListView(translator, { q: "minds" }, { rows: [], nextCursor: null });
  assert.equal(empty.state, "empty");
  if (empty.state === "empty") {
    assert.equal(empty.empty, "No arguments match this search.");
  }

  const merged = mergeArgumentSearchPage(translator, "en-US", [], { items: [RESULT], next_cursor: "c2" });
  const ready = argumentSearchListView(translator, { q: "minds" }, merged);
  assert.equal(ready.state, "ready");
  if (ready.state === "ready") {
    assert.equal(ready.rows.length, 1);
    assert.equal(ready.more, "Show more results");
    assert.equal(ready.end, null);
  }
});

test("a late answer is discarded, never merged", () => {
  assert.equal(isStaleArgumentResponse(1, 2), true);
  assert.equal(isStaleArgumentResponse(2, 2), false);
  assert.equal(isStaleArgumentResponse(3, 2), false);
});

test("a source check refuses the malicious and waves the plain", () => {
  for (const malicious of [
    "",
    "   ",
    "javascript:alert(1)",
    "JaVaScRiPt:alert(1)",
    "data:text/html,<p>x</p>",
    "vbscript:msgbox(1)",
    "ftp://example.test/file",
    "/relative/path",
    "example.test/no-scheme",
    "https://user:pass@example.test/",
    "https://user@example.test/",
    "https://exam ple.test/",
    "https://example.test/a b",
    "http://",
    "https://exämple.test/",
  ]) {
    assert.equal(isSafeSourceUrl(malicious), false, `must refuse: ${malicious}`);
  }
  for (const plain of [
    "https://example.test/ethics",
    "http://example.test:8080/a?b=c#d",
    "HTTPS://EXAMPLE.TEST/UPPER",
    "https://142.250.0.1/ip",
  ]) {
    assert.equal(isSafeSourceUrl(plain), true, `must wave: ${plain}`);
  }
  assert.equal(isSafeSourceUrl("  https://example.test/trimmed  "), true, "surrounding space trims");
});

test("argument failures name the real server codes and fall back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.ok(argumentFailure(translator, "argument_invalid_relation").includes("listed relations"));
  assert.ok(argumentFailure(translator, "invalid_query").includes("did not pass"));
  assert.ok(argumentFailure(translator, "invalid_cursor").includes("expired"));
  assert.ok(argumentFailure(translator, "argument_not_found").includes("removed"));
  assert.ok(argumentFailure(translator, "arena_not_found").includes("not been published"));
  assert.equal(
    argumentFailure(translator, "argument_too_many"),
    argumentFailure(translator, "something-the-backend-never-emits"),
    "an unknown code must fall back to the generic sentence",
  );
});
