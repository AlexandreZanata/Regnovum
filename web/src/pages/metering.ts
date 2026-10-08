/**
 * Metering staged presentation (P57-T02, harness-only).
 *
 * The four INK metering operations quote one publication candidate,
 * confirm it with its exact charge, and read the receipt and the owner
 * extract. What lives in this module, DOM-free so the Node runner
 * verifies it without a browser, are the translated views over the
 * staged documents plus the guards that keep the browser honest: the
 * quote renders exactly what the server priced — units, totals,
 * version, hashes and the acceptance window — and any altered byte
 * prices anew instead of reusing the preview; confirmation is explicit
 * under a single-flight guard and never an optimistic debit; the
 * receipt is checked original against the preview terms; and the
 * balance is the journal-derived figure the server sent, never a
 * browser subtraction.
 *
 * Amounts are integers in thousandths that never pass through binary
 * floating point: a value outside the safe range throws instead of
 * rendering a rounded charge. Dates render through `Intl` in UTC.
 * The surface stays staged: the production composition enables no
 * capability, so the gate renders the honest unavailability view and
 * the page sends nothing — above all, never a confirmation. Enabling
 * the capability still grants no authorization: the server decides
 * authentication, price, cutoff, ownership and replay on every call.
 * Every string comes from the catalog the runtime serves; the
 * component that renders a view translates nothing and formats
 * nothing itself.
 */
import type {
  MeteringPublication,
  MeteringQuote,
  MeteringReceipt,
  MeteringStatement,
} from "../contracts/staged/metering.js";
import { isStagedEnabled } from "../core/staged.js";
import type { StagedCapabilities } from "../core/staged.js";
import { formatInstant, formatNumber, instantOf } from "../i18n/formats.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";
import { requireStagedEnabled, stagedUnavailableView } from "./staged.js";
import type { StagedUnavailableView } from "./staged.js";

/**
 * assertSafeMilli refuses anything that is not an exact integer amount.
 * JSON carries every quantity as a `Number`, so a value outside the
 * safe range — or a fractional one — throws instead of rendering a
 * rounded charge. `bigint` always passes: it is already exact.
 */
export function assertSafeMilli(value: number | bigint): number | bigint {
  if (typeof value === "bigint") {
    return value;
  }
  if (!Number.isSafeInteger(value)) {
    throw new TypeError(`INK thousandths must be a safe integer, received ${String(value)}`);
  }
  return value;
}

/** formatMilli renders integer thousandths with the locale's grouping. */
export function formatMilli(locale: Locale, value: number | bigint): string {
  return formatNumber(locale, assertSafeMilli(value));
}

/** Everything the page renders for one priced preview. */
export interface MeteringQuoteView {
  readonly heading: string;
  readonly total: string;
  readonly version: string;
  /** Rule version the server priced under, verbatim for the confirm echo. */
  readonly versionNumber: number;
  /** Content hash the server priced, verbatim for the freshness check. */
  readonly contentHash: string;
  readonly window: string;
  readonly confirm: string;
  readonly retry: string;
  /** True while `now` is still inside the acceptance window. */
  readonly live: boolean;
  readonly expiryNote: string | null;
}

/** Everything the page renders for one settlement. */
export interface MeteringPublicationView {
  readonly heading: string;
  readonly line: string;
  readonly replayed: boolean;
}

/** One receipt leg: verbatim direction/kind/label with its amount. */
export interface MeteringLegView {
  readonly direction: string;
  readonly kind: string;
  readonly label: string;
  readonly amount: string;
}

/** Everything the page renders for one owned receipt. */
export interface MeteringReceiptView {
  readonly heading: string;
  readonly total: string;
  readonly posted: string;
  readonly refund: string | null;
  readonly legs: readonly MeteringLegView[];
  /** The publication identity, byte-identical for reconciliation. */
  readonly publicationId: string;
}

/** One extract line: the owned publication with its posted charge. */
export interface MeteringStatementRow {
  readonly publicationId: string;
  readonly service: string;
  readonly total: string;
  readonly posted: string;
  readonly refunded: boolean;
}

/** What the page renders for an extract: rows or nothing yet. */
export type MeteringStatementView =
  | { readonly state: "ready"; readonly heading: string; readonly balance: string; readonly rows: readonly MeteringStatementRow[] }
  | { readonly state: "empty"; readonly heading: string; readonly empty: string };

/**
 * isQuoteLive answers whether `now` is still inside the acceptance
 * window. A lapsed quote never confirms: the person requests a new
 * preview instead. An unparseable instant throws instead of rendering
 * a guessed window.
 */
export function isQuoteLive(expiresAt: string, now: Date | number): boolean {
  const expires = instantOf(expiresAt).getTime();
  const at = now instanceof Date ? now.getTime() : now;
  return at < expires;
}

/**
 * needsFreshQuote answers whether the candidate changed since the
 * preview priced it. Any altered byte — content or service — prices
 * anew: the confirmation must carry exactly what the preview priced,
 * never edited terms under an old window.
 */
export function needsFreshQuote(previewedContent: string, currentContent: string, previewedService: string, currentService: string): boolean {
  return previewedContent !== currentContent || previewedService !== currentService;
}

/**
 * isOriginalReceipt answers whether a receipt still carries the preview
 * terms: same total, same content hash and same rule version. A
 * divergent receipt is a defect to surface, never a charge to confirm
 * quietly.
 */
export function isOriginalReceipt(quote: MeteringQuote, receipt: MeteringReceipt): boolean {
  return (
    receipt.total_milli === quote.total_milli &&
    receipt.content_hash === quote.content_hash &&
    receipt.version === quote.version
  );
}

/** quoteView projects one priced preview for one locale in UTC. */
export function quoteView(
  translator: Translator,
  locale: Locale,
  quote: MeteringQuote,
  now: Date | number,
): MeteringQuoteView {
  const live = isQuoteLive(quote.expires_at, now);
  return {
    heading: translator.translate("metering.quote.title"),
    total: translator.translate("metering.quote.total", {
      total: formatMilli(locale, quote.total_milli),
      units: formatMilli(locale, quote.units),
    }),
    version: formatMilli(locale, quote.version),
    versionNumber: quote.version,
    contentHash: quote.content_hash,
    window: translator.translate("metering.quote.expires", {
      date: formatInstant(locale, quote.expires_at, {
        dateStyle: "medium",
        timeStyle: "short",
        timeZone: "UTC",
      }),
    }),
    confirm: translator.translate("metering.quote.confirm"),
    retry: translator.translate("metering.quote.retry"),
    live,
    expiryNote: live ? null : translator.translate("metering.quote.lapsed"),
  };
}

/** publicationView projects one settlement for one locale in UTC. */
export function publicationView(
  translator: Translator,
  locale: Locale,
  publication: MeteringPublication,
): MeteringPublicationView {
  return {
    heading: translator.translate("metering.receipt.title"),
    line: translator.translate("metering.receipt.posted", {
      date: formatInstant(locale, publication.posted_at, {
        dateStyle: "medium",
        timeStyle: "short",
        timeZone: "UTC",
      }),
    }),
    replayed: publication.replayed,
  };
}

/** receiptView projects one owned receipt for one locale in UTC. */
export function receiptView(
  translator: Translator,
  locale: Locale,
  receipt: MeteringReceipt,
): MeteringReceiptView {
  return {
    heading: translator.translate("metering.receipt.title"),
    total: translator.translate("metering.receipt.total", {
      total: formatMilli(locale, receipt.total_milli),
      units: formatMilli(locale, receipt.units),
    }),
    posted: translator.translate("metering.receipt.posted", {
      date: formatInstant(locale, receipt.posted_at, {
        dateStyle: "medium",
        timeStyle: "short",
        timeZone: "UTC",
      }),
    }),
    refund:
      receipt.refund === undefined
        ? null
        : translator.translate("metering.receipt.refunded", {
            date: formatInstant(locale, receipt.refund.posted_at, {
              dateStyle: "medium",
              timeStyle: "short",
              timeZone: "UTC",
            }),
          }),
    legs: receipt.legs.map((leg) => ({
      direction: leg.direction,
      kind: leg.kind,
      label: leg.label,
      amount: formatMilli(locale, leg.amount_milli),
    })),
    publicationId: receipt.publication_id,
  };
}

/**
 * statementView projects one owner extract for one locale. The balance
 * is the journal-derived figure the server sent — never a browser
 * subtraction, never an optimistic debit: unsent confirmations change
 * nothing here until the server answers and the extract is re-read.
 */
export function statementView(
  translator: Translator,
  locale: Locale,
  statement: MeteringStatement,
): MeteringStatementView {
  const heading = translator.translate("metering.statement.title");
  if (statement.entries.length === 0) {
    return { state: "empty", heading, empty: translator.translate("metering.statement.empty") };
  }
  return {
    state: "ready",
    heading,
    balance: translator.translate("metering.statement.balance", {
      total: formatMilli(locale, statement.balance_milli),
    }),
    rows: statement.entries.map((entry) => ({
      publicationId: entry.publication_id,
      service: entry.service,
      total: formatMilli(locale, entry.total_milli),
      posted: formatInstant(locale, entry.posted_at, {
        dateStyle: "medium",
        timeStyle: "short",
        timeZone: "UTC",
      }),
      refunded: entry.refunded,
    })),
  };
}

/**
 * A single-flight guard for one confirmation: the first press while
 * idle opens the flight, any press inside it is refused, and the
 * answer — success or failure — closes it. The guard keeps no text
 * and sends nothing; it only decides whether this press may become a
 * request, so a double click never settles twice.
 */
export interface ConfirmGuard {
  /** Opens the flight when idle; false refuses a press already flying. */
  tryBegin(): boolean;
  /** Closes the flight after the answer arrives. */
  release(): void;
}

/** createConfirmGuard builds one guard, idle at first. */
export function createConfirmGuard(): ConfirmGuard {
  let flying = false;
  return {
    tryBegin: (): boolean => {
      if (flying) {
        return false;
      }
      flying = true;
      return true;
    },
    release: (): void => {
      flying = false;
    },
  };
}

/**
 * meteringFailure translates a refusal by the server code the backend
 * really emits. A missing session refuses with `unauthorized`, an
 * acceptance for another account or content with `quote_mismatch`, a
 * lapsed window with `quote_expired`, an uncovered price with
 * `price_unavailable`, a divergent key reuse with `publish_conflict`
 * and a foreign row with `publication_unknown`. Anything else falls
 * back to the generic sentence — request a new quote — instead of
 * inventing a meaning.
 */
export function meteringFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "unauthorized":
      return translator.translate("metering.failure.unauthorized");
    case "quote_mismatch":
      return translator.translate("metering.failure.mismatch");
    case "quote_expired":
      return translator.translate("metering.failure.expired");
    case "price_unavailable":
      return translator.translate("metering.failure.expired");
    case "publish_conflict":
      return translator.translate("metering.failure.conflict");
    case "publication_unknown":
      return translator.translate("metering.failure.missing");
    default:
      return translator.translate("metering.failure.generic");
  }
}

/**
 * meteringGate answers the staged metering page: an enabled capability
 * returns no view and the caller may read, a disabled one returns the
 * honest unavailability view and the caller sends nothing — above all,
 * never a confirmation. The gate reads only the capabilities value —
 * never the DOM, the URL or storage.
 */
export function meteringGate(
  capabilities: StagedCapabilities,
  translator: Translator,
): { readonly enabled: boolean; readonly view: StagedUnavailableView | null } {
  if (isStagedEnabled(capabilities, "metering")) {
    return { enabled: true, view: null };
  }
  return { enabled: false, view: stagedUnavailableView(translator, "metering") };
}

/**
 * requireMeteringEnabled guards every metering call: a disabled page
 * throws instead of sending. The throw carries the feature so the
 * caller renders the same unavailability view it would have rendered
 * without calling.
 */
export function requireMeteringEnabled(capabilities: StagedCapabilities): void {
  requireStagedEnabled(capabilities, "metering");
}
