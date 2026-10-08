/**
 * Commerce staged presentation (P57-T03, harness-only).
 *
 * The two trade escrow reads show one owned receipt — parties, status,
 * season key, amounts with the tithe split and compensations — and the
 * participant extract with owned lines. What lives in this module,
 * DOM-free so the Node runner verifies it without a browser, are the
 * translated views over the staged documents plus the explanation of
 * the accepted terminal policy: terminal contracts carry the
 * no-effect note, contracts in course carry the competent-authority
 * note, and anything undecidable carries the custody-preserved note.
 * The page explains the policy the domain accepted; it never decides
 * litigation in the browser — no evidence is classified here, no
 * release or refund is inferred, and no judgment is rendered.
 *
 * Amounts are integers in thousandths that never pass through binary
 * floating point: a value outside the safe range throws instead of
 * rendering a rounded escrow. Only the contract's own fields render —
 * gross, tithe, net, refunded and the refund legs — so no interest,
 * market rate, Crown title or Crown expense is ever added. Parties,
 * roles, statuses and keys travel verbatim; dates render through
 * `Intl` in UTC. The surface stays staged: the production composition
 * enables no capability, so the gate renders the honest unavailability
 * view and the page sends nothing. Enabling the capability still
 * grants no authorization: the server decides authentication,
 * ownership and state on every call. Every string comes from the
 * catalog the runtime serves; the component that renders a view
 * translates nothing and formats nothing itself.
 */
import type { TradeReceipt, TradeStatement } from "../contracts/staged/commerce.js";
import { isStagedEnabled } from "../core/staged.js";
import type { StagedCapabilities } from "../core/staged.js";
import { formatInstant, formatNumber } from "../i18n/formats.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";
import { requireStagedEnabled, stagedUnavailableView } from "./staged.js";
import type { StagedUnavailableView } from "./staged.js";

/**
 * assertSafeMilli refuses anything that is not an exact integer amount.
 * JSON carries every quantity as a `Number`, so a value outside the
 * safe range — or a fractional one — throws instead of rendering a
 * rounded escrow. `bigint` always passes: it is already exact.
 */
export function assertSafeMilli(value: number | bigint): number | bigint {
  if (typeof value === "bigint") {
    return value;
  }
  if (!Number.isSafeInteger(value)) {
    throw new TypeError(`trade thousandths must be a safe integer, received ${String(value)}`);
  }
  return value;
}

/** formatMilli renders integer thousandths with the locale's grouping. */
export function formatMilli(locale: Locale, value: number | bigint): string {
  return formatNumber(locale, assertSafeMilli(value));
}

/** Escrow statuses the fragment declares, in domain order. */
export const TRADE_STATUSES: readonly string[] = [
  "funded",
  "accepted",
  "released",
  "refunded",
  "expired",
  "resolved",
];

/** Terminal escrow statuses: released, refunded and resolved never reopen. */
export const TERMINAL_TRADE_STATUSES: readonly string[] = ["released", "refunded", "resolved"];

/**
 * isTerminalTradeStatus answers whether a verbatim status settled
 * terminally. It mirrors the domain's closed machine without
 * classifying any evidence: the browser explains, never decides.
 */
export function isTerminalTradeStatus(status: string): boolean {
  return (TERMINAL_TRADE_STATUSES as readonly string[]).includes(status);
}

/**
 * terminalNote explains the accepted terminal policy for one verbatim
 * status. Terminal contracts keep the no-effect note, contracts in
 * course keep the competent-authority note, and an unknown status —
 * or anything the allowlist never declared — keeps the blocked note:
 * custody preserved, no new judgment inferred.
 */
export function terminalNote(translator: Translator, status: string): string {
  if (isTerminalTradeStatus(status)) {
    return translator.translate("commerce.receipt.settled_note");
  }
  if ((TRADE_STATUSES as readonly string[]).includes(status)) {
    return translator.translate("commerce.receipt.pending_note");
  }
  return translator.translate("commerce.receipt.blocked_note");
}

/** One refund leg of a receipt: verbatim key with its amounts. */
export interface TradeRefundView {
  readonly key: string;
  readonly amount: string;
  readonly posted: string;
}

/** Everything the page renders for one owned receipt. */
export interface TradeReceiptView {
  readonly heading: string;
  /** Contract key, byte-identical: never translated, never shaped. */
  readonly key: string;
  /** Party role, verbatim: buyer and provider read their own side. */
  readonly role: string;
  /** Traded object, byte-identical: never translated, never shaped. */
  readonly object: string;
  readonly gross: string;
  readonly tithe: string;
  readonly net: string;
  readonly refunded: string;
  readonly status: string;
  readonly policy: string;
  readonly posted: string;
  readonly settled: string | null;
  readonly expires: string;
  /** Escrow identity, byte-identical for reconciliation. */
  readonly escrow: string;
  readonly refunds: readonly TradeRefundView[];
}

/** One extract line: the owned contract with its posted charge. */
export interface TradeStatementRow {
  readonly contractId: string;
  readonly key: string;
  readonly role: string;
  readonly gross: string;
  readonly status: string;
  readonly policy: string;
  readonly posted: string;
}

/** What the page renders for an extract: one page of rows, or nothing yet. */
export type TradeStatementView =
  | { readonly state: "ready"; readonly heading: string; readonly rows: readonly TradeStatementRow[] }
  | { readonly state: "empty"; readonly heading: string; readonly empty: string };

/** receiptView projects one owned receipt for one locale in UTC. */
export function receiptView(
  translator: Translator,
  locale: Locale,
  receipt: TradeReceipt,
): TradeReceiptView {
  return {
    heading: translator.translate("commerce.receipt.title"),
    key: receipt.contract_key,
    role: translator.translate("commerce.receipt.role_line", { role: receipt.role }),
    object: receipt.object,
    gross: translator.translate("commerce.receipt.gross", { total: formatMilli(locale, receipt.gross_milli) }),
    tithe: translator.translate("commerce.receipt.tithe", { total: formatMilli(locale, receipt.tithe_milli) }),
    net: translator.translate("commerce.receipt.net", { total: formatMilli(locale, receipt.net_milli) }),
    refunded: formatMilli(locale, receipt.refunded_milli),
    status: translator.translate("commerce.receipt.status_line", { status: receipt.status }),
    policy: terminalNote(translator, receipt.status),
    posted: translator.translate("commerce.receipt.posted", {
      date: formatInstant(locale, receipt.posted_at, {
        dateStyle: "medium",
        timeStyle: "short",
        timeZone: "UTC",
      }),
    }),
    settled:
      receipt.settled_at === undefined
        ? null
        : translator.translate("commerce.receipt.settled", {
            date: formatInstant(locale, receipt.settled_at, {
              dateStyle: "medium",
              timeStyle: "short",
              timeZone: "UTC",
            }),
          }),
    expires: formatInstant(locale, receipt.expires_at, {
      dateStyle: "medium",
      timeStyle: "short",
      timeZone: "UTC",
    }),
    escrow: receipt.escrow_transfer_id,
    refunds: receipt.refunds.map((refund) => ({
      key: refund.refund_key,
      amount: formatMilli(locale, refund.amount_milli),
      posted: formatInstant(locale, refund.posted_at, {
        dateStyle: "medium",
        timeStyle: "short",
        timeZone: "UTC",
      }),
    })),
  };
}

/**
 * statementView projects one participant extract for one locale. Rows
 * keep the server order — the extract arrives newest first in one
 * page of at most fifty lines — and the page never re-sorts it and
 * invents no cursor: the fragment declares no pagination parameter,
 * so the page reads the page it received.
 */
export function statementView(
  translator: Translator,
  locale: Locale,
  statement: TradeStatement,
): TradeStatementView {
  const heading = translator.translate("commerce.statement.title");
  if (statement.entries.length === 0) {
    return { state: "empty", heading, empty: translator.translate("commerce.statement.empty") };
  }
  return {
    state: "ready",
    heading,
    rows: statement.entries.map((entry) => ({
      contractId: entry.contract_id,
      key: entry.contract_key,
      role: entry.role,
      gross: formatMilli(locale, entry.gross_milli),
      status: translator.translate("commerce.receipt.status_line", { status: entry.status }),
      policy: terminalNote(translator, entry.status),
      posted: formatInstant(locale, entry.posted_at, {
        dateStyle: "medium",
        timeStyle: "short",
        timeZone: "UTC",
      }),
    })),
  };
}

/**
 * commerceFailure translates a refusal by the server code the backend
 * really emits. A missing session refuses with `unauthorized`, a
 * foreign or unknown contract with `contract_unknown` and a malformed
 * receipt request with `receipt_invalid`. Anything else falls back to
 * the generic sentence instead of inventing a meaning.
 */
export function commerceFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "unauthorized":
      return translator.translate("commerce.failure.unauthorized");
    case "contract_unknown":
      return translator.translate("commerce.failure.missing");
    case "receipt_invalid":
      return translator.translate("commerce.failure.invalid");
    default:
      return translator.translate("commerce.failure.generic");
  }
}

/**
 * commerceGate answers the staged commerce page: an enabled capability
 * returns no view and the caller may read, a disabled one returns the
 * honest unavailability view and the caller sends nothing. The gate
 * reads only the capabilities value — never the DOM, the URL or
 * storage.
 */
export function commerceGate(
  capabilities: StagedCapabilities,
  translator: Translator,
): { readonly enabled: boolean; readonly view: StagedUnavailableView | null } {
  if (isStagedEnabled(capabilities, "commerce")) {
    return { enabled: true, view: null };
  }
  return { enabled: false, view: stagedUnavailableView(translator, "commerce") };
}

/**
 * requireCommerceEnabled guards every commerce read: a disabled page
 * throws instead of sending. The throw carries the feature so the
 * caller renders the same unavailability view it would have rendered
 * without calling.
 */
export function requireCommerceEnabled(capabilities: StagedCapabilities): void {
  requireStagedEnabled(capabilities, "commerce");
}
