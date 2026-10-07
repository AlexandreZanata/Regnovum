/**
 * Arena pass entitlement and history presentation (P54-T02).
 *
 * The account reads two safe contracts and renders nothing it was
 * not given. `summaryPresentation` projects the derived summary —
 * the available total the server counted without the expired lots,
 * the checked instant rendered by `Intl`, and one sentence per
 * lot with its contracted quantity, its real remainder, its
 * recorded origin and its immutable expiration. `passHistoryEntryView`
 * projects one consumption line as a single catalog sentence: the
 * stable origin vocabulary and the Arena identifier verbatim, with
 * the instant rendered — and the reference beside it for the owner
 * to reconcile. Internal lot identifiers and provider payloads
 * never serialize, so none render either.
 *
 * Three refusals shape this module. Counts are integers that never
 * pass through binary floating point: a `Number` outside the safe
 * range is refused instead of rounded. The expiration is the
 * server's derivation, not the page's: a lot whose `expired` flag
 * the server set renders as expired even when its `expires_at`
 * looks future, a null `expires_at` renders as never expiring, and
 * switching the interface locale only re-renders the same instant
 * — it never moves it. And the MEMBER lots are the projected
 * Member state, rendered exactly as derived: the browser grants
 * nothing, renews nothing, and a reset re-reads instead of
 * creating a pass. Every string comes from the catalog the runtime
 * serves; the component that renders a view translates nothing
 * and formats nothing.
 */
import { formatInstant, formatNumber } from "../i18n/formats.js";
import type {
  ArenaPassHistoryEntry,
  ArenaPassLot,
  ArenaPassSummary,
} from "../contracts/generated.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";

/** Page size the contract sizes: 1..100, defaulting to 20. */
const HISTORY_LIMIT_MIN = 1;
const HISTORY_LIMIT_MAX = 100;

/** Everything the entitlement view renders for the derived summary. */
export interface PassSummaryView {
  readonly heading: string;
  readonly intro: string;
  readonly available: string;
  readonly checked: string;
  readonly lots: readonly string[];
  readonly empty: string | null;
}

/** Everything the page renders for one consumption line. */
export interface PassHistoryEntryView {
  readonly line: string;
  readonly reference: string;
}

/**
 * assertSafeCount refuses anything that is not an exact integer
 * count. JSON carries every quantity as a `Number`, so a value
 * outside the safe range — or a fractional one — throws instead
 * of rendering a rounded entitlement.
 */
export function assertSafeCount(value: number | bigint): number | bigint {
  if (typeof value === "bigint") {
    return value;
  }
  if (!Number.isSafeInteger(value)) {
    throw new TypeError(`pass counts must be a safe integer, received ${String(value)}`);
  }
  return value;
}

/** formatCount renders whole units with the locale's grouping. */
export function formatCount(locale: Locale, value: number | bigint): string {
  return formatNumber(locale, assertSafeCount(value));
}

/**
 * lotView projects one lot for one locale. The origin stays
 * verbatim — it is the server's stable vocabulary — and the
 * expiration follows the derived flag: expired renders expired,
 * a null deadline renders as never expiring, otherwise the
 * deadline renders as the fixed instant it is.
 */
export function lotView(translator: Translator, locale: Locale, lot: ArenaPassLot): string {
  const remaining = formatCount(locale, lot.remaining);
  const quantity = formatCount(locale, lot.quantity);
  if (lot.expires_at === null) {
    return translator.translate("wallet.passes.lot_no_expiry", {
      remaining,
      quantity,
      origin: lot.origin,
    });
  }
  const instant = formatInstant(locale, lot.expires_at, { dateStyle: "medium", timeStyle: "short" });
  if (lot.expired) {
    return translator.translate("wallet.passes.lot_expired", {
      remaining,
      quantity,
      origin: lot.origin,
      instant,
    });
  }
  return translator.translate("wallet.passes.lot_active", {
    remaining,
    quantity,
    origin: lot.origin,
    instant,
  });
}

/**
 * summaryPresentation projects the derived summary for one
 * locale. Expired lots stay listed — the owner sees what lapsed —
 * but the available total is the server's count without them.
 */
export function summaryPresentation(
  translator: Translator,
  locale: Locale,
  summary: ArenaPassSummary,
): PassSummaryView {
  const lots = summary.lots.map((lot) => lotView(translator, locale, lot));
  return {
    heading: translator.translate("wallet.passes.heading"),
    intro: translator.translate("wallet.passes.intro"),
    available: translator.translate("wallet.passes.available", {
      count: formatCount(locale, summary.available_total),
    }),
    checked: translator.translate("wallet.passes.checked", {
      instant: formatInstant(locale, summary.checked_at, { dateStyle: "medium", timeStyle: "short" }),
    }),
    lots,
    empty: summary.lots.length === 0 ? translator.translate("wallet.passes.empty") : null,
  };
}

/**
 * passHistoryEntryView projects one consumption line for one
 * locale. The origin and the Arena identifier stay verbatim —
 * stable vocabulary and opaque identifier, never translated —
 * and the reference travels beside the sentence.
 */
export function passHistoryEntryView(
  translator: Translator,
  locale: Locale,
  entry: ArenaPassHistoryEntry,
): PassHistoryEntryView {
  return {
    line: translator.translate("wallet.passes.entry", {
      origin: entry.origin,
      arena: entry.arena_id,
      instant: formatInstant(locale, entry.consumed_at, { dateStyle: "medium", timeStyle: "short" }),
    }),
    reference: entry.reference,
  };
}

/** passHistoryEmpty renders the empty consumption history. */
export function passHistoryEmpty(translator: Translator): string {
  return translator.translate("wallet.passes.empty");
}

/**
 * historyLimit keeps the page size only inside the page the
 * contract sizes. Anything outside 1..100 is dropped instead of
 * sent to fail: the canonical address holds what the server
 * accepts, and the default page applies.
 */
export function historyLimit(value: string | null): number | undefined {
  if (value === null || value === "") {
    return undefined;
  }
  const parsed = Number.parseInt(value, 10);
  if (!Number.isInteger(parsed) || parsed < HISTORY_LIMIT_MIN || parsed > HISTORY_LIMIT_MAX) {
    return undefined;
  }
  return parsed;
}

/**
 * historyCursor keeps the opaque cursor only when present.
 * It is never parsed and never built — only passed back.
 */
export function historyCursor(value: string | null): string | undefined {
  if (value === null || value === "") {
    return undefined;
  }
  return value;
}

/**
 * mergeHistoryEntries keeps one history page honest across
 * reloads and late answers: entries the owner already holds keep
 * their place, newcomers append once, and duplicates collapse on
 * the stable consumption identifier.
 */
export function mergeHistoryEntries(
  existing: readonly ArenaPassHistoryEntry[],
  incoming: readonly ArenaPassHistoryEntry[],
): readonly ArenaPassHistoryEntry[] {
  const seen = new Set(existing.map((entry) => entry.consumption_id));
  const merged: ArenaPassHistoryEntry[] = [...existing];
  for (const entry of incoming) {
    if (!seen.has(entry.consumption_id)) {
      seen.add(entry.consumption_id);
      merged.push(entry);
    }
  }
  return merged;
}

/**
 * passesFailure translates a refusal by the server code the
 * backend really emits. The billing adapter answers
 * `invalid_cursor` for a forged cursor, `invalid_limit` for a
 * non-numeric page size and `unauthorized` without a session;
 * anything else falls back to the generic sentence instead of
 * inventing a meaning. The wallet namespace owns these
 * sentences, so the pass surface reuses them.
 */
export function passesFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "invalid_cursor":
      return translator.translate("wallet.failure.invalid_cursor");
    case "invalid_limit":
      return translator.translate("wallet.failure.invalid_limit");
    case "unauthorized":
      return translator.translate("wallet.failure.unauthorized");
    default:
      return translator.translate("wallet.failure.generic");
  }
}
