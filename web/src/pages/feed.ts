/**
 * Feed and search presentation (P52-T01).
 *
 * The home, feed and search pages share one discovery journey over the
 * existing arenas client: the feed reads `getArenaFeed`, the search
 * reads `searchArenas`, and both render newest-first cursor pages the
 * person extends deliberately — there is no autosave, no prefetch and
 * no polling here. What lives in this module, DOM-free so the Node
 * runner verifies it without a browser, are the three things such a
 * renderer needs: the allowlisted reading of filters from the URL, the
 * translated view of one result row, and the page merge that keeps a
 * list honest across reloads and late answers.
 *
 * The vocabularies below are the contract's, not the page's: language,
 * category and status only accept the values `api/openapi.json`
 * declares, the limit only the 1..100 page the contract sizes, and the
 * cursor travels opaque — never parsed, never built, only passed back.
 * Anything outside the vocabulary is dropped from the URL instead of
 * sent to fail: the canonical address holds what the server accepts.
 * User content is never translated: statements, slugs, categories and
 * identifiers render byte-identical, through text nodes downstream, and
 * the relevance score stays out of the view — it is the server's
 * ordering signal, not a fact the page shows. Item links are not
 * assembled here either: the document address belongs to the public
 * Arena page (P52-T02), and this module only carries the slug that
 * names it.
 */
import { formatInstant } from "../i18n/formats.js";
import type { ArenaFeed, PublicArenaSummary, SearchArenaPage, SearchArenaResult } from "../contracts/generated.js";
import type { ArenaFeedQuery, ArenaSearchQuery } from "../core/clients/arenas.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";

/** Content languages the contract declares for the feed and the search. */
const FEED_LANGUAGES: readonly string[] = ["pt-BR", "en-US"];

/** Editorial categories the contract declares for the feed. */
const FEED_CATEGORIES: readonly string[] = [
  "technology",
  "science",
  "philosophy",
  "politics",
  "economics",
  "health",
  "culture",
  "society",
];

/** Publicly visible statuses the contract declares for the feed. */
const FEED_STATUSES: readonly string[] = ["published", "closed", "restricted"];

/** Page size the contract sizes: 1..100, defaulting to 20. */
const FEED_LIMIT_MIN = 1;
const FEED_LIMIT_MAX = 100;

/** Keeps the value only when it belongs to the closed vocabulary. */
function vocabularyValue(value: string | null, vocabulary: readonly string[]): string | undefined {
  if (value === null || value === "") {
    return undefined;
  }
  return vocabulary.includes(value) ? value : undefined;
}

/** Keeps the limit only inside the page the contract sizes. */
function pageLimit(value: string | null): number | undefined {
  if (value === null || value === "") {
    return undefined;
  }
  const parsed = Number.parseInt(value, 10);
  if (!Number.isInteger(parsed) || parsed < FEED_LIMIT_MIN || parsed > FEED_LIMIT_MAX) {
    return undefined;
  }
  return parsed;
}

/** Keeps the cursor only when one travels: opaque, never parsed. */
function opaqueCursor(value: string | null): string | undefined {
  if (value === null || value === "") {
    return undefined;
  }
  return value;
}

/**
 * parseFeedQuery reads the feed filters from a query string. Only the
 * allowlisted keys survive, only inside the contract vocabularies; the
 * cursor travels opaque and the limit only inside 1..100.
 */
export function parseFeedQuery(search: string): ArenaFeedQuery {
  const params = new URLSearchParams(search);
  const language = vocabularyValue(params.get("language"), FEED_LANGUAGES);
  const category = vocabularyValue(params.get("category"), FEED_CATEGORIES);
  const status = vocabularyValue(params.get("status"), FEED_STATUSES);
  const cursor = opaqueCursor(params.get("cursor"));
  const limit = pageLimit(params.get("limit"));
  return {
    ...(language === undefined ? {} : { language }),
    ...(category === undefined ? {} : { category }),
    ...(status === undefined ? {} : { status }),
    ...(cursor === undefined ? {} : { cursor }),
    ...(limit === undefined ? {} : { limit }),
  };
}

/**
 * serializeFeedQuery writes the canonical address of one feed state:
 * the keys the contract declares, nothing else, so back, forward and
 * reload show the filters the server accepts.
 */
export function serializeFeedQuery(query: ArenaFeedQuery): string {
  const params = new URLSearchParams();
  if (query.language !== undefined) {
    params.set("language", query.language);
  }
  if (query.category !== undefined) {
    params.set("category", query.category);
  }
  if (query.status !== undefined) {
    params.set("status", query.status);
  }
  if (query.cursor !== undefined) {
    params.set("cursor", query.cursor);
  }
  if (query.limit !== undefined) {
    params.set("limit", String(query.limit));
  }
  return params.toString();
}

/**
 * parseSearchQuery reads one search from a query string. A blank query
 * is not a search: it answers null so the page shows its hint instead
 * of fetching. Anything else travels verbatim — Unicode included — for
 * the server to judge against its 1..200 rule.
 */
export function parseSearchQuery(search: string): (ArenaSearchQuery & { readonly q: string }) | null {
  const params = new URLSearchParams(search);
  const raw = params.get("q");
  const q = raw === null ? "" : raw.trim();
  if (q === "") {
    return null;
  }
  const language = vocabularyValue(params.get("language"), FEED_LANGUAGES);
  const cursor = opaqueCursor(params.get("cursor"));
  const limit = pageLimit(params.get("limit"));
  return {
    q,
    ...(language === undefined ? {} : { language }),
    ...(cursor === undefined ? {} : { cursor }),
    ...(limit === undefined ? {} : { limit }),
  };
}

/**
 * serializeSearchQuery writes the canonical address of one search
 * state, with the same allowlist the feed keeps.
 */
export function serializeSearchQuery(query: ArenaSearchQuery & { readonly q: string }): string {
  const params = new URLSearchParams();
  params.set("q", query.q);
  if (query.language !== undefined) {
    params.set("language", query.language);
  }
  if (query.cursor !== undefined) {
    params.set("cursor", query.cursor);
  }
  if (query.limit !== undefined) {
    params.set("limit", String(query.limit));
  }
  return params.toString();
}

/** Everything the page renders for one feed row. */
export interface FeedRowView {
  /** Stable identifier, for the merge — never a link target. */
  readonly id: string;
  /** The slug that names the document; the address belongs to P52-T02. */
  readonly slug: string;
  /** User content, byte-identical: never translated, never shaped. */
  readonly statement: string;
  /** Editorial code, verbatim: the page translates no taxonomy. */
  readonly category: string;
  readonly status: string;
  readonly language: string;
  readonly published: string;
}

/** Everything the page renders for one search row: no status to show. */
export interface SearchRowView {
  /** Stable identifier, for the merge — never a link target. */
  readonly id: string;
  /** The slug that names the document; the address belongs to P52-T02. */
  readonly slug: string;
  /** User content, byte-identical: never translated, never shaped. */
  readonly statement: string;
  /** Editorial code, verbatim: the page translates no taxonomy. */
  readonly category: string;
  readonly language: string;
  readonly published: string;
}

/** feedRow projects one feed item for one locale. */
export function feedRow(translator: Translator, locale: Locale, item: PublicArenaSummary): FeedRowView {
  return {
    id: item.id,
    slug: item.slug,
    statement: item.statement,
    category: item.category,
    status: translator.translate(`arenas.document.status.${item.status}`),
    language: item.language,
    published: formatInstant(locale, item.published_at, { dateStyle: "medium", timeStyle: "short" }),
  };
}

/**
 * searchRow projects one search result for one locale. The result
 * carries no public status in the contract, so the row shows none —
 * and the relevance score stays out with it.
 */
export function searchRow(locale: Locale, item: SearchArenaResult): SearchRowView {
  return {
    id: item.id,
    slug: item.slug,
    statement: item.statement,
    category: item.category,
    language: item.language,
    published: formatInstant(locale, item.published_at, { dateStyle: "medium", timeStyle: "short" }),
  };
}

/** One honest page of rows: the items and the cursor that extends them. */
export interface RowPage<Row> {
  readonly rows: readonly Row[];
  /** The next cursor, or null when the list ends. */
  readonly nextCursor: string | null;
}

/**
 * mergeFeedPage extends a rendered list with one feed answer: rows the
 * list already shows are never duplicated, order stays newest-first,
 * and the next cursor is the answer's — the only cursor the page may
 * send.
 */
export function mergeFeedPage(
  translator: Translator,
  locale: Locale,
  existing: readonly FeedRowView[],
  page: ArenaFeed,
): RowPage<FeedRowView> {
  const seen = new Set(existing.map((row) => row.id));
  const rows = [...existing];
  for (const item of page.items) {
    if (!seen.has(item.id)) {
      seen.add(item.id);
      rows.push(feedRow(translator, locale, item));
    }
  }
  return { rows, nextCursor: page.next_cursor };
}

/**
 * mergeSearchPage extends a rendered list with one search answer, with
 * the same no-duplication the feed keeps.
 */
export function mergeSearchPage(
  locale: Locale,
  existing: readonly SearchRowView[],
  page: SearchArenaPage,
): RowPage<SearchRowView> {
  const seen = new Set(existing.map((row) => row.id));
  const rows = [...existing];
  for (const item of page.items) {
    if (!seen.has(item.id)) {
      seen.add(item.id);
      rows.push(searchRow(locale, item));
    }
  }
  return { rows, nextCursor: page.next_cursor };
}

/**
 * isStaleResponse answers whether an answer arrived too late to render:
 * each request takes the next sequence number, and only the newest one
 * owns the screen. A stale answer is discarded, never merged.
 */
export function isStaleResponse(answerSequence: number, newestSequence: number): boolean {
  return answerSequence < newestSequence;
}

/** What the page renders for a list: rows, an end, or nothing yet. */
export type FeedListView =
  | { readonly state: "ready"; readonly rows: readonly FeedRowView[]; readonly more: string | null; readonly end: string | null }
  | { readonly state: "empty"; readonly empty: string };

/** feedListView projects one merged feed list for one translator. */
export function feedListView(translator: Translator, merged: RowPage<FeedRowView>): FeedListView {
  if (merged.rows.length === 0) {
    return { state: "empty", empty: translator.translate("arenas.feed.empty") };
  }
  return {
    state: "ready",
    rows: merged.rows,
    more: merged.nextCursor === null ? null : translator.translate("arenas.feed.load_more"),
    end: merged.nextCursor === null ? translator.translate("arenas.feed.end") : null,
  };
}

/** What the page renders for results: rows, an end, a hint, or nothing yet. */
export type SearchListView =
  | { readonly state: "ready"; readonly rows: readonly SearchRowView[]; readonly more: string | null; readonly end: string | null }
  | { readonly state: "empty"; readonly empty: string }
  | { readonly state: "hint"; readonly hint: string };

/** searchListView projects one merged search list for one translator. */
export function searchListView(
  translator: Translator,
  query: (ArenaSearchQuery & { readonly q: string }) | null,
  merged: RowPage<SearchRowView>,
): SearchListView {
  if (query === null) {
    return { state: "hint", hint: translator.translate("arenas.search.hint") };
  }
  if (merged.rows.length === 0) {
    return { state: "empty", empty: translator.translate("arenas.search.empty") };
  }
  return {
    state: "ready",
    rows: merged.rows,
    more: merged.nextCursor === null ? null : translator.translate("arenas.search.load_more"),
    end: merged.nextCursor === null ? translator.translate("arenas.search.end") : null,
  };
}

/**
 * feedFailure translates a refusal by the server code the backend
 * really emits. Codes the backend never emits are not named here: they
 * fall back to the generic sentence instead of inventing a meaning.
 */
export function feedFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "invalid_cursor":
      return translator.translate("arenas.failure.invalid_cursor");
    default:
      return translator.translate("arenas.failure.generic");
  }
}
