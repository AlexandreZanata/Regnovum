/**
 * Seasons staged presentation (P57-T01, harness-only).
 *
 * The four season reads show the current ACTIVE season, the allowlisted
 * history, one allowlisted season and its privacy-safe champions. What lives
 * in this module, DOM-free so the Node runner verifies it without a browser,
 * are the translated views over the staged documents plus the guards that
 * keep the browser honest: server dates render in UTC, the countdown expires
 * without extending the cutoff, history carries no spendable balance, and a
 * game office is never an administrator grant.
 *
 * Dates and durations come from the server alone: `starts_at`/`ends_at`
 * render through `Intl` in UTC and the expiry answers only whether `now`
 * reached `ends_at` — reaching it renders the expired sentence, never a new
 * window. History rows are the server's allowlist in ordinal order with no
 * balance, holder, account or ledger field ever read. Champions reuse
 * `championsPresentation`: pseudonym displays only, exact wealth never
 * reaches the shape, leaders keep the server order, and the cutoff revision
 * travels as the count the server sealed.
 *
 * The surface stays staged: the production composition enables no
 * capability, so the gate renders the honest unavailability view and the
 * page sends nothing. Enabling the capability still grants no
 * authorization — the server decides authentication, namespace, state and
 * cutoff on every call. Every string comes from the catalog the runtime
 * serves; the component that renders a view translates nothing and formats
 * nothing itself.
 */
import { championsPresentation } from "../components/seasons/model.js";
import type { SeasonChampionsExhibit } from "../components/seasons/model.js";
import type {
  SeasonChampionsDocument,
  SeasonDocument,
  SeasonHistoryDocument,
} from "../contracts/staged/seasons.js";
import { isStagedEnabled } from "../core/staged.js";
import type { StagedCapabilities } from "../core/staged.js";
import { formatInstant, formatNumber, instantOf } from "../i18n/formats.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";
import { requireStagedEnabled, stagedUnavailableView } from "./staged.js";
import type { StagedUnavailableView } from "./staged.js";

/** Everything the page renders for one season document. */
export interface SeasonDetailView {
  readonly heading: string;
  /** Book identity, byte-identical: never translated, never shaped. */
  readonly key: string;
  readonly ordinal: string;
  readonly period: string;
  /** Lifecycle state label with the verbatim server state beside it. */
  readonly state: string;
  /** True once `now` reached the server `ends_at`: the cutoff never moves. */
  readonly expired: boolean;
  readonly expiryNote: string | null;
}

/** One history row: identity, dates and state only — never a balance. */
export interface SeasonHistoryRow {
  readonly key: string;
  readonly ordinal: string;
  readonly period: string;
  readonly state: string;
}

/** What the page renders for a history answer: rows or nothing yet. */
export type SeasonHistoryView =
  | { readonly state: "ready"; readonly heading: string; readonly count: string; readonly rows: readonly SeasonHistoryRow[] }
  | { readonly state: "empty"; readonly heading: string; readonly empty: string };

/** Everything the page renders for one champions document. */
export interface SeasonChampionsView {
  readonly heading: string;
  readonly cutoff: string;
  /** Integrity hash, byte-identical: never translated, never shaped. */
  readonly hash: string;
  readonly exhibit: SeasonChampionsExhibit;
  readonly empty: string | null;
}

/**
 * isSeasonLive answers whether `now` is still before the server `ends_at`.
 * Reaching the cutoff ends the display: the browser never extends it and a
 * client timer never reopens it. An unparseable instant throws instead of
 * rendering a guessed window.
 */
export function isSeasonLive(endsAt: string, now: Date | number): boolean {
  const ends = instantOf(endsAt).getTime();
  const at = now instanceof Date ? now.getTime() : now;
  return at < ends;
}

/**
 * isStaleSeasonsResponse answers whether an answer arrived too late to
 * render: each request takes the next sequence number, and only the newest
 * one owns the screen. A stale answer is discarded, never merged.
 */
export function isStaleSeasonsResponse(answerSequence: number, newestSequence: number): boolean {
  return answerSequence < newestSequence;
}

/** seasonDetailView projects one season document for one locale in UTC. */
export function seasonDetailView(
  translator: Translator,
  locale: Locale,
  document: SeasonDocument,
  now: Date | number,
): SeasonDetailView {
  const start = formatInstant(locale, document.starts_at, {
    dateStyle: "medium",
    timeStyle: "short",
    timeZone: "UTC",
  });
  const end = formatInstant(locale, document.ends_at, {
    dateStyle: "medium",
    timeStyle: "short",
    timeZone: "UTC",
  });
  const expired = !isSeasonLive(document.ends_at, now);
  return {
    heading: translator.translate("seasons.detail.title"),
    key: document.season_key,
    ordinal: translator.translate("seasons.detail.ordinal", {
      ordinal: formatNumber(locale, document.ordinal),
    }),
    period: translator.translate("seasons.detail.period", { start, end }),
    state: translator.translate("seasons.detail.state_line", { state: document.state }),
    expired,
    expiryNote: expired ? translator.translate("seasons.detail.expired") : null,
  };
}

/** seasonHistoryRow projects one allowlisted season for one locale in UTC. */
export function seasonHistoryRow(
  translator: Translator,
  locale: Locale,
  document: SeasonDocument,
  now: Date | number,
): SeasonHistoryRow {
  const detail = seasonDetailView(translator, locale, document, now);
  return { key: detail.key, ordinal: detail.ordinal, period: detail.period, state: detail.state };
}

/**
 * seasonHistoryView projects one history answer for one locale. Rows keep
 * the server order — the allowlist arrives in ordinal order and the page
 * never re-sorts it — and carry no balance: the history is an allowlist,
 * never a ledger extract, so no spendable amount renders here.
 */
export function seasonHistoryView(
  translator: Translator,
  locale: Locale,
  history: SeasonHistoryDocument,
  now: Date | number,
): SeasonHistoryView {
  const heading = translator.translate("seasons.history.title");
  if (history.seasons.length === 0) {
    return { state: "empty", heading, empty: translator.translate("seasons.history.empty") };
  }
  return {
    state: "ready",
    heading,
    count: translator.translate("seasons.history.count", {
      count: formatNumber(locale, history.seasons.length),
    }),
    rows: history.seasons.map((season) => seasonHistoryRow(translator, locale, season, now)),
  };
}

/**
 * seasonChampionsView projects one redacted champions document for one
 * locale. Titles and pseudonyms travel verbatim from the server answer —
 * already localized by `Accept-Language` there — while the leaders keep
 * their sealed order through `championsPresentation`. Exact wealth never
 * reaches this shape: the HTTP surface hides it, so the browser cannot leak
 * the íntegra. The game office (`last_king`) is a game cargo, never an
 * administrator grant, and renders as a pseudonym beside its title.
 */
export function seasonChampionsView(
  translator: Translator,
  locale: Locale,
  document: SeasonChampionsDocument,
): SeasonChampionsView {
  const exhibit = championsPresentation({
    title: document.title,
    richest: document.richest_title,
    lastKing: translator.translate("seasons.champions.holder", {
      title: document.last_king_title,
      holder: document.last_king,
    }),
    leaders: document.leaders.map((leader) => leader.display),
  });
  return {
    heading: translator.translate("seasons.champions.title"),
    cutoff: translator.translate("seasons.champions.cutoff", {
      revision: formatNumber(locale, document.cutoff_revision),
    }),
    hash: document.hash,
    exhibit,
    empty: document.leaders.length === 0 ? translator.translate("seasons.champions.empty") : null,
  };
}

/**
 * seasonFailure translates a refusal by the server code the backend really
 * emits. A missing session refuses with `unauthorized`, the explicitly
 * inactive namespace with `season_mismatch`, an unknown book with
 * `season_unknown`, and reads without ACTIVE with `season_closed` or
 * `season_archived`. Anything else falls back to the generic sentence
 * instead of inventing a meaning.
 */
export function seasonFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "unauthorized":
      return translator.translate("seasons.failure.unauthorized");
    case "season_mismatch":
      return translator.translate("seasons.failure.mismatch");
    case "season_unknown":
      return translator.translate("seasons.failure.missing");
    case "season_closed":
      return translator.translate("seasons.failure.closed");
    case "season_archived":
      return translator.translate("seasons.failure.archived");
    default:
      return translator.translate("seasons.failure.generic");
  }
}

/**
 * seasonsGate answers the staged seasons page: an enabled capability returns
 * no view and the caller may read, a disabled one returns the honest
 * unavailability view and the caller sends nothing. The gate reads only the
 * capabilities value — never the DOM, the URL or storage.
 */
export function seasonsGate(
  capabilities: StagedCapabilities,
  translator: Translator,
): { readonly enabled: boolean; readonly view: StagedUnavailableView | null } {
  if (isStagedEnabled(capabilities, "seasons")) {
    return { enabled: true, view: null };
  }
  return { enabled: false, view: stagedUnavailableView(translator, "seasons") };
}

/**
 * requireSeasonsEnabled guards every seasons read: a disabled page throws
 * instead of sending. The throw carries the feature so the caller renders
 * the same unavailability view it would have rendered without calling.
 */
export function requireSeasonsEnabled(capabilities: StagedCapabilities): void {
  requireStagedEnabled(capabilities, "seasons");
}
