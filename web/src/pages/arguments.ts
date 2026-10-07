/**
 * Argument reading and discovery presentation (P53-T02).
 *
 * The list, the single read and the full-text search share one
 * discovery journey over the arguments client: the list reads
 * `listArenaArguments` newest-first per relation, the read fetches
 * `getPublicArgument`, and the search reads `searchArguments` with
 * the query travelling verbatim for the server to judge against its
 * 1..200 rule. What lives in this module, DOM-free so the Node
 * runner verifies it without a browser, are the four things such a
 * renderer needs: the relation vocabulary with its labels, the
 * translated view of one row, the page merge that keeps a list
 * honest across reloads and late answers, and the structural source
 * check the publication form will reuse.
 *
 * Nothing here duplicates the feed module: the query allowlist
 * readers, the opaque cursor, the stale-answer guard and the row
 * page shape arrive by import, and the relation labels are the
 * catalog's own (`arenas.participation.relation.*`). User content is
 * never translated: contents render byte-identical through text
 * nodes downstream, the search score stays out of the view — it is
 * the server's ordering signal, not a fact the page shows — and the
 * search never changes the Arena language: each result carries its
 * own, rendered as-is.
 *
 * Withdrawn and removed arguments never appear in a list. A withdrawn
 * single read resolves as a retracted placeholder — identity and
 * status stay visible while the content is withheld — and a removed
 * one reads as not found.
 */
import { publishFailure } from "./publication.js";
import type { ArgumentSearchQuery } from "../core/clients/arguments.js";
import type {
  Argument,
  ArgumentListItem,
  ArgumentPage,
  SearchArgumentPage,
  SearchArgumentResult,
} from "../contracts/generated.js";
import { formatInstant } from "../i18n/formats.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";
import {
  FEED_LANGUAGES,
  isStaleResponse,
  opaqueCursor,
  pageLimit,
  vocabularyValue,
  type RowPage,
} from "./feed.js";

/** Relations the contract declares for an argument. */
export const ARGUMENT_RELATIONS: readonly string[] = ["support", "oppose", "context"];

/** Source URL bounds the domain declares (structural, mirrored). */
const SOURCE_URL_MIN_LENGTH = 8;
const SOURCE_URL_MAX_LENGTH = 2048;

/** relationLabel names one relation in the locale of the page. */
export function relationLabel(translator: Translator, relation: string): string {
  switch (relation) {
    case "support":
      return translator.translate("arenas.participation.relation.support");
    case "oppose":
      return translator.translate("arenas.participation.relation.oppose");
    case "context":
      return translator.translate("arenas.participation.relation.context");
    default:
      return relation;
  }
}

/**
 * parseArgumentSearch reads one argument search from a query string. A
 * blank query is not a search: it answers null so the page shows its
 * hint instead of fetching. Anything else travels verbatim — Unicode
 * included — for the server to judge against its 1..200 rule; the
 * language filter only narrows, it never changes any Arena language.
 */
export function parseArgumentSearch(search: string): (ArgumentSearchQuery & { readonly q: string }) | null {
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
 * serializeArgumentSearch writes the canonical address of one search
 * state, with the same allowlist the feed keeps.
 */
export function serializeArgumentSearch(query: ArgumentSearchQuery & { readonly q: string }): string {
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

/** Everything the page renders for one listed argument. */
export interface ArgumentRowView {
  readonly id: string;
  readonly arenaId: string;
  /** Relation label, translated: the code itself never renders. */
  readonly relation: string;
  /** The content, verbatim — lists never carry withdrawn content. */
  readonly content: string | null;
  readonly replies: number;
  readonly status: string;
  readonly created: string;
}

/** argumentRow projects one list item for one locale. */
export function argumentRow(translator: Translator, locale: Locale, item: ArgumentListItem): ArgumentRowView {
  return {
    id: item.id,
    arenaId: item.arena_id,
    relation: relationLabel(translator, item.relation),
    content: item.content,
    replies: item.reply_count,
    status: item.status,
    created: formatInstant(locale, item.created_at, { dateStyle: "medium", timeStyle: "short" }),
  };
}

/** Everything the page renders for one single read. */
export interface ArgumentView {
  readonly id: string;
  readonly arenaId: string;
  readonly relation: string;
  /**
   * The content, verbatim — or null when the argument was withdrawn:
   * the placeholder keeps identity and status while withholding it.
   */
  readonly content: string | null;
  readonly status: string;
  /** The retraction sentence when withdrawn, else null. */
  readonly withdrawnNote: string | null;
  readonly created: string;
}

/** argumentView projects one public argument for one locale. */
export function argumentView(translator: Translator, locale: Locale, argument: Argument): ArgumentView {
  return {
    id: argument.id,
    arenaId: argument.arena_id,
    relation: relationLabel(translator, argument.relation),
    content: argument.content,
    status: argument.status,
    withdrawnNote:
      argument.status === "withdrawn" ? translator.translate("arenas.arguments.withdrawn_note") : null,
    created: formatInstant(locale, argument.created_at, { dateStyle: "medium", timeStyle: "short" }),
  };
}

/** Everything the page renders for one search result. */
export interface ArgumentSearchRowView {
  readonly id: string;
  readonly arenaId: string;
  /** User content, byte-identical: never translated, never shaped. */
  readonly content: string;
  readonly relation: string;
  /** The result's own language, rendered as-is: the search changes none. */
  readonly language: string;
  readonly created: string;
}

/**
 * argumentSearchRow projects one search result for one locale. The
 * relevance score stays out of the view: it ordered the page, and
 * ordering is not a fact the page shows.
 */
export function argumentSearchRow(
  translator: Translator,
  locale: Locale,
  item: SearchArgumentResult,
): ArgumentSearchRowView {
  return {
    id: item.id,
    arenaId: item.arena_id,
    content: item.content,
    relation: relationLabel(translator, item.relation),
    language: item.language,
    created: formatInstant(locale, item.created_at, { dateStyle: "medium", timeStyle: "short" }),
  };
}

/**
 * mergeArgumentPage extends a rendered list with one list answer:
 * rows the list already shows are never duplicated, order stays
 * newest-first, and the next cursor is the answer's.
 */
export function mergeArgumentPage(
  translator: Translator,
  locale: Locale,
  existing: readonly ArgumentRowView[],
  page: ArgumentPage,
): RowPage<ArgumentRowView> {
  const seen = new Set(existing.map((row) => row.id));
  const rows = [...existing];
  for (const item of page.items) {
    if (!seen.has(item.id)) {
      seen.add(item.id);
      rows.push(argumentRow(translator, locale, item));
    }
  }
  return { rows, nextCursor: page.next_cursor };
}

/**
 * mergeArgumentSearchPage extends a rendered list with one search
 * answer, with the same no-duplication the list keeps.
 */
export function mergeArgumentSearchPage(
  translator: Translator,
  locale: Locale,
  existing: readonly ArgumentSearchRowView[],
  page: SearchArgumentPage,
): RowPage<ArgumentSearchRowView> {
  const seen = new Set(existing.map((row) => row.id));
  const rows = [...existing];
  for (const item of page.items) {
    if (!seen.has(item.id)) {
      seen.add(item.id);
      rows.push(argumentSearchRow(translator, locale, item));
    }
  }
  return { rows, nextCursor: page.next_cursor };
}

/**
 * isStaleArgumentResponse answers whether an answer arrived too late
 * to render. It is the feed's sequence guard, shared — not
 * reimplemented — so a page turn or a new search that overtook an
 * older request discards it instead of merging it.
 */
export function isStaleArgumentResponse(answerSequence: number, newestSequence: number): boolean {
  return isStaleResponse(answerSequence, newestSequence);
}

/** What the page renders for a list: rows, an end, or nothing yet. */
export type ArgumentListView =
  | { readonly state: "ready"; readonly rows: readonly ArgumentRowView[]; readonly more: string | null; readonly end: string | null }
  | { readonly state: "empty"; readonly empty: string };

/**
 * argumentListView projects one merged list for one translator. The
 * empty sentence is the catalog's own: withdrawn and removed
 * arguments never appear, so an empty relation simply has none yet.
 */
export function argumentListView(translator: Translator, merged: RowPage<ArgumentRowView>): ArgumentListView {
  if (merged.rows.length === 0) {
    return { state: "empty", empty: translator.translate("arenas.participation.arguments.empty") };
  }
  return {
    state: "ready",
    rows: merged.rows,
    more: merged.nextCursor === null ? null : translator.translate("arenas.search.load_more"),
    end: merged.nextCursor === null ? translator.translate("arenas.search.end") : null,
  };
}

/** What the page renders for results: rows, an end, a hint, or nothing yet. */
export type ArgumentSearchListView =
  | { readonly state: "ready"; readonly rows: readonly ArgumentSearchRowView[]; readonly more: string | null; readonly end: string | null }
  | { readonly state: "empty"; readonly empty: string }
  | { readonly state: "hint"; readonly hint: string };

/** argumentSearchListView projects one merged search list for one translator. */
export function argumentSearchListView(
  translator: Translator,
  query: (ArgumentSearchQuery & { readonly q: string }) | null,
  merged: RowPage<ArgumentSearchRowView>,
): ArgumentSearchListView {
  if (query === null) {
    return { state: "hint", hint: translator.translate("arenas.arguments.search_hint") };
  }
  if (merged.rows.length === 0) {
    return { state: "empty", empty: translator.translate("arenas.arguments.search_empty") };
  }
  return {
    state: "ready",
    rows: merged.rows,
    more: merged.nextCursor === null ? null : translator.translate("arenas.search.load_more"),
    end: merged.nextCursor === null ? translator.translate("arenas.search.end") : null,
  };
}

/**
 * isSafeSourceUrl is the structural courtesy check a publication form
 * runs before sending a source: an absolute http(s) address without
 * credentials, whitespace or control characters, inside the domain
 * bounds, over an ASCII host. It mirrors the server domain
 * (`ParseSourceURL`) without fetching or resolving anything — the
 * server stays the only authority, and anything this check waves
 * through still faces it.
 */
export function isSafeSourceUrl(raw: string): boolean {
  const trimmed = raw.trim();
  if (trimmed === "") {
    return false;
  }
  if (trimmed.length < SOURCE_URL_MIN_LENGTH || trimmed.length > SOURCE_URL_MAX_LENGTH) {
    return false;
  }
  for (const char of trimmed) {
    if (char === " " || /\\s/.test(char)) {
      return false;
    }
    const code = char.codePointAt(0) ?? 0;
    if (code < 0x20 || code === 0x7f) {
      return false;
    }
  }
  const separator = trimmed.indexOf("://");
  if (separator <= 0) {
    return false;
  }
  const scheme = trimmed.slice(0, separator).toLowerCase();
  if (scheme !== "http" && scheme !== "https") {
    return false;
  }
  const rest = trimmed.slice(separator + 3);
  const authorityEnd = rest.search(/[/?#]/);
  const authority = authorityEnd < 0 ? rest : rest.slice(0, authorityEnd);
  if (authority === "" || authority.includes("@")) {
    return false;
  }
  for (const char of authority) {
    const code = char.codePointAt(0) ?? 0;
    if (code > 0x7e || code < 0x21) {
      return false;
    }
  }
  return true;
}

/** What the page renders for replies: rows, an end, or nothing yet. */
export type ArgumentRepliesView =
  | { readonly state: "ready"; readonly rows: readonly ArgumentRowView[]; readonly more: string | null; readonly end: string | null }
  | { readonly state: "empty"; readonly empty: string };

/**
 * argumentRepliesView projects one merged replies page. Replies are
 * rows of the same shape — the depth policy forbids grandchildren,
 * so there is no deeper level to render.
 */
export function argumentRepliesView(
  translator: Translator,
  merged: RowPage<ArgumentRowView>,
): ArgumentRepliesView {
  if (merged.rows.length === 0) {
    return { state: "empty", empty: translator.translate("arenas.arguments.replies_empty") };
  }
  return {
    state: "ready",
    rows: merged.rows,
    more: merged.nextCursor === null ? null : translator.translate("arenas.search.load_more"),
    end: merged.nextCursor === null ? translator.translate("arenas.search.end") : null,
  };
}

/** Everything the page confirms before withdrawing one argument. */
export interface WithdrawConfirmView {
  /**
   * Names the content under withdrawal: retracting removes it from
   * display without erasing the historical fact and without any
   * refund, and the sentence says exactly that.
   */
  readonly confirmation: string;
  readonly submit: string;
}

/**
 * withdrawConfirmView projects the sentence the page confirms with.
 * Only the author's own argument is withdrawable; a stranger's
 * reads as not found and a removed one can never be overridden,
 * both before this screen. After the withdrawal the page re-reads:
 * the placeholder — never an optimistic removal — is what renders.
 */
export function withdrawConfirmView(translator: Translator, argument: Argument): WithdrawConfirmView {
  return {
    confirmation: translator.translate("arenas.arguments.withdraw_confirm", {
      content: argument.content ?? "",
    }),
    submit: translator.translate("arenas.arguments.withdraw_submit"),
  };
}

/** Everything the page renders above the reply form. */
export interface ReplyContextView {
  /** Names the parent content the reply answers, verbatim. */
  readonly notice: string;
}

/**
 * replyContextView projects the parent one reply answers: the Arena
 * travels in the path so a parent outside it stays refused, and the
 * depth policy accepts a single level — a reply to a reply is
 * refused before any write.
 */
export function replyContextView(translator: Translator, parent: Argument): ReplyContextView {
  return {
    notice: translator.translate("arenas.arguments.reply_to", { content: parent.content ?? "" }),
  };
}

/**
 * argumentFailure translates a refusal by the server code the backend
 * really emits. Codes the backend never emits are not named here:
 * they fall back to the generic sentence instead of inventing a
 * meaning.
 */
export function argumentFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "argument_invalid_relation":
      return translator.translate("arenas.participation.errors.invalid_relation");
    case "invalid_query":
      return translator.translate("arenas.arguments.failure_invalid_query");
    case "invalid_cursor":
      return translator.translate("arenas.failure.invalid_cursor");
    case "argument_not_found":
      return translator.translate("arenas.arguments.failure_missing");
    case "arena_not_found":
      return translator.translate("arenas.document.not_found.detail");
    default:
      return translator.translate("arenas.arguments.failure_generic");
  }
}

/**
 * replyFailure translates a refusal of a reply or a withdrawal. Codes
 * the publication journey already names — content, relation,
 * sources, balance, eligibility and arena state — arrive by import
 * instead of a second switch; only the reply and withdrawal codes
 * live here.
 */
export function replyFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "parent_not_found":
      return translator.translate("arenas.arguments.failure_parent_missing");
    case "parent_not_available":
      return translator.translate("arenas.arguments.failure_parent_unavailable");
    case "reply_depth_exceeded":
      return translator.translate("arenas.arguments.failure_depth");
    case "argument_not_withdrawable":
      return translator.translate("arenas.arguments.failure_not_withdrawable");
    case "argument_not_found":
      return translator.translate("arenas.arguments.failure_missing");
    default:
      return publishFailure(translator, serverCode);
  }
}
