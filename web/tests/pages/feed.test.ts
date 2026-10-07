/**
 * Tests of the feed and search presentation (P52-T01).
 *
 * They run the real generated catalogs in both locales: filters read
 * from the URL inside the contract vocabularies only, a blank search
 * shows its hint instead of fetching, user content renders
 * byte-identical without translation, pages merge without duplication,
 * stale answers are discarded, and an invalid cursor names its refusal.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  feedFailure,
  feedListView,
  feedRow,
  isStaleResponse,
  mergeFeedPage,
  mergeSearchPage,
  parseFeedQuery,
  parseSearchQuery,
  searchListView,
  searchRow,
  serializeFeedQuery,
  serializeSearchQuery,
} from "../../src/pages/feed.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type {
  ArenaFeed,
  PublicArenaSummary,
  SearchArenaPage,
  SearchArenaResult,
} from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespace the discovery pages render from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["arenas"] });
}

function feedItem(id: string, statement: string): PublicArenaSummary {
  return {
    id,
    slug: `arena-${id}`,
    statement,
    category: "philosophy",
    language: "pt-BR",
    published_at: "2026-09-20T10:00:00Z",
    status: "published",
  };
}

function searchItem(id: string, statement: string): SearchArenaResult {
  return {
    id,
    slug: `arena-${id}`,
    statement,
    category: "philosophy",
    language: "pt-BR",
    published_at: "2026-09-20T10:00:00Z",
    score: 0.9,
  };
}

const PAGE_ONE: ArenaFeed = { items: [feedItem("a1", "Primeira controvérsia"), feedItem("a2", "Segunda")], next_cursor: "cursor-2" };
const PAGE_TWO: ArenaFeed = { items: [feedItem("a2", "Segunda"), feedItem("a3", "Terceira")], next_cursor: null };

test("the feed reads its filters inside the contract vocabularies", () => {
  const query = parseFeedQuery("?language=pt-BR&category=philosophy&status=published&cursor=opaque-1&limit=20");

  assert.deepEqual(query, {
    language: "pt-BR",
    category: "philosophy",
    status: "published",
    cursor: "opaque-1",
    limit: 20,
  });
});

test("the feed drops what the contract never declares", () => {
  const query = parseFeedQuery("?language=fr&category=gossip&status=draft&limit=500&evil=1&cursor=");

  assert.deepEqual(query, {});
  assert.equal(serializeFeedQuery(query), "");
});

test("the feed address round-trips back, forward and reload", () => {
  const query = parseFeedQuery("?category=science&status=closed&limit=50&cursor=opaque-9&language=en-US");

  assert.equal(serializeFeedQuery(query), "language=en-US&category=science&status=closed&cursor=opaque-9&limit=50");
  assert.deepEqual(parseFeedQuery(`?${serializeFeedQuery(query)}`), query);
});

test("a blank search is a hint, never a fetch", () => {
  assert.equal(parseSearchQuery(""), null);
  assert.equal(parseSearchQuery("?q=++"), null);

  const view = searchListView(translatorOf("pt-BR"), null, { rows: [], nextCursor: null });
  assert.equal(view.state, "hint");
  if (view.state === "hint") {
    assert.equal(view.hint, "Digite o que procura para buscar.");
  } else {
    assert.ok(false, "a blank search must show its hint");
  }
});

test("a search travels verbatim, Unicode included", () => {
  const query = parseSearchQuery("?q=busca+%C3%A9tica&language=pt-BR");

  assert.equal(query?.q, "busca ética");
  assert.equal(query?.language, "pt-BR");
  assert.ok(serializeSearchQuery(query!).includes("q=busca+%C3%A9tica"), "the address must keep the query");
});

test("user content renders byte-identical without translation", () => {
  const statement = "arenas.feed.empty {instant} <b>nada</b>";
  const row = feedRow(translatorOf("en-US"), "en-US", feedItem("a9", statement));
  const found = searchRow("en-US", searchItem("s9", statement));

  assert.equal(row.statement, statement);
  assert.equal(found.statement, statement);
  assert.equal(row.category, "philosophy", "the taxonomy is shown, never translated");
  assert.equal(row.status, "Published", "the status label is translated; the content is not");
  const serialized = JSON.stringify(row);
  assert.ok(!serialized.includes("score"), "the relevance score stays out of the view");
});

test("pages merge without duplication and the cursor comes from the answer", () => {
  const translator = translatorOf("pt-BR");
  const first = mergeFeedPage(translator, "pt-BR", [], PAGE_ONE);
  assert.equal(first.rows.length, 2);
  assert.equal(first.nextCursor, "cursor-2");

  const second = mergeFeedPage(translator, "pt-BR", first.rows, PAGE_TWO);
  assert.deepEqual(second.rows.map((row) => row.id), ["a1", "a2", "a3"]);
  assert.equal(second.nextCursor, null);

  const view = feedListView(translator, second);
  assert.equal(view.state, "ready");
  if (view.state === "ready") {
    assert.equal(view.more, null);
    assert.equal(view.end, "Você chegou ao fim da lista.");
  } else {
    assert.ok(false, "a merged list must be ready");
  }
});

test("search pages merge and name their end in en-US", () => {
  const merged = mergeSearchPage("en-US", [], {
    items: [searchItem("s1", "One"), searchItem("s1", "One")],
    next_cursor: "cursor-2",
  } as SearchArenaPage);

  assert.deepEqual(merged.rows.map((row) => row.id), ["s1"]);
  const view = searchListView(translatorOf("en-US"), { q: "one" }, merged);
  assert.equal(view.state, "ready");
  if (view.state === "ready") {
    assert.equal(view.more, "Show more results");
    assert.equal(view.end, null);
  } else {
    assert.ok(false, "a merged search must be ready");
  }
});

test("an empty feed names its empty state", () => {
  const view = feedListView(translatorOf("pt-BR"), { rows: [], nextCursor: null });

  assert.equal(view.state, "empty");
  if (view.state === "empty") {
    assert.equal(view.empty, "Nenhuma arena por aqui, com estes filtros.");
  } else {
    assert.ok(false, "an empty feed must name its empty state");
  }
});

test("a stale answer is discarded, never merged", () => {
  assert.equal(isStaleResponse(1, 2), true);
  assert.equal(isStaleResponse(2, 2), false);
  assert.equal(isStaleResponse(3, 2), false);
});

test("an invalid cursor names its refusal and falls back otherwise", () => {
  const translator = translatorOf("en-US");

  assert.equal(feedFailure(translator, "invalid_cursor"), "This page expired. Go back to the start of the list.");
  assert.equal(
    feedFailure(translator, "cursor_expired"),
    feedFailure(translator, "something-the-backend-never-emits"),
    "an unknown code must fall back to the generic sentence",
  );
});
