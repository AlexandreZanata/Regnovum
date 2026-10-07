/**
 * Wallet and ledger-statement presentation (P54-T01).
 *
 * The account reads two safe contracts and renders nothing it was
 * not given. `balancePresentation` projects the derived bucket
 * balances — FREE_INK consumed first, PURCHASED_INK after — in
 * whole INK units with the locale's grouping, and the factual
 * total of the two. `statementEntryView` projects one ledger line
 * as a single catalog sentence: the signed amount, the stable
 * operation and bucket vocabularies verbatim, and the instant
 * rendered by `Intl` instead of shown as the RFC 3339 string the
 * contract carries. The reference travels verbatim beside it;
 * there is no receipt URL in the contract to follow.
 *
 * Two refusals shape this module. Amounts are integers that never
 * pass through binary floating point: a `Number` outside the safe
 * range is refused instead of rounded, because an amount that
 * cannot be represented exactly is a defect to fix at the boundary,
 * not to format quietly. And the page invents no legacy/seasonal
 * split: `api/openapi.json` carries only `balance_free` and
 * `balance_purchased`, so a third bucket would be a fiction the
 * server could not honor — the buckets below are the contract's,
 * not the page's. Every string comes from the catalog the runtime
 * serves; the component that renders a view translates nothing
 * and formats nothing.
 */
import { formatInstant, formatNumber } from "../i18n/formats.js";
import type { WalletBalance, WalletStatementEntry } from "../contracts/generated.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";

/** Page size the contract sizes: 1..100, defaulting to 20. */
const STATEMENT_LIMIT_MIN = 1;
const STATEMENT_LIMIT_MAX = 100;

/** Everything the wallet view renders for the derived balances. */
export interface WalletBalanceView {
  readonly heading: string;
  readonly intro: string;
  readonly free: string;
  readonly purchased: string;
  readonly total: string;
}

/** Everything the page renders for one ledger line. */
export interface WalletEntryView {
  readonly line: string;
  readonly reference: string;
}

/**
 * assertSafeInk refuses anything that is not an exact integer count.
 * JSON carries every quantity as a `Number`, so a value outside the
 * safe range — or a fractional one — throws instead of rendering a
 * rounded balance. `bigint` always passes: it is already exact.
 */
export function assertSafeInk(value: number | bigint): number | bigint {
  if (typeof value === "bigint") {
    return value;
  }
  if (!Number.isSafeInteger(value)) {
    throw new TypeError(`INK units must be a safe integer, received ${String(value)}`);
  }
  return value;
}

/** formatInk renders whole INK units with the locale's grouping. */
export function formatInk(locale: Locale, value: number | bigint): string {
  return formatNumber(locale, assertSafeInk(value));
}

/**
 * balancePresentation projects the derived balances for one locale.
 * The total is the factual sum of the two buckets the contract
 * declares, assembled with `BigInt` so the addition itself never
 * loses a unit.
 */
export function balancePresentation(
  translator: Translator,
  locale: Locale,
  balance: WalletBalance,
): WalletBalanceView {
  const free = assertSafeInk(balance.balance_free);
  const purchased = assertSafeInk(balance.balance_purchased);
  const total = BigInt(typeof free === "bigint" ? free : BigInt(free)) + BigInt(typeof purchased === "bigint" ? purchased : BigInt(purchased));
  return {
    heading: translator.translate("wallet.balance.heading"),
    intro: translator.translate("wallet.balance.intro"),
    free: translator.translate("wallet.balance.free", { amount: formatInk(locale, free) }),
    purchased: translator.translate("wallet.balance.purchased", { amount: formatInk(locale, purchased) }),
    total: translator.translate("wallet.balance.total", { amount: formatInk(locale, total) }),
  };
}

/**
 * statementEntryView projects one ledger line for one locale.
 * The operation and bucket stay verbatim — they are the server's
 * stable vocabulary, not translated content — and the reference
 * travels beside the sentence for the owner to reconcile.
 */
export function statementEntryView(
  translator: Translator,
  locale: Locale,
  entry: WalletStatementEntry,
): WalletEntryView {
  const signed = formatSignedInk(locale, entry.amount);
  return {
    line: translator.translate("wallet.statement.entry", {
      signed,
      operation: entry.operation_type,
      bucket: entry.bucket,
      instant: formatInstant(locale, entry.created_at, { dateStyle: "medium", timeStyle: "short" }),
    }),
    reference: entry.reference,
  };
}

/** formatSignedInk renders a signed ledger delta with its sign. */
function formatSignedInk(locale: Locale, amount: number | bigint): string {
  const safe = assertSafeInk(amount);
  if (typeof safe === "bigint") {
    if (safe < 0n) {
      return `-${formatInk(locale, -safe)}`;
    }
    return `+${formatInk(locale, safe)}`;
  }
  if (safe < 0) {
    return `-${formatInk(locale, -safe)}`;
  }
  return `+${formatInk(locale, safe)}`;
}

/** statementEmpty renders the empty ledger in one sentence. */
export function statementEmpty(translator: Translator): string {
  return translator.translate("wallet.statement.empty");
}

/**
 * statementLimit keeps the page size only inside the page the
 * contract sizes. Anything outside 1..100 is dropped instead of
 * sent to fail: the canonical address holds what the server
 * accepts, and the default page applies.
 */
export function statementLimit(value: string | null): number | undefined {
  if (value === null || value === "") {
    return undefined;
  }
  const parsed = Number.parseInt(value, 10);
  if (!Number.isInteger(parsed) || parsed < STATEMENT_LIMIT_MIN || parsed > STATEMENT_LIMIT_MAX) {
    return undefined;
  }
  return parsed;
}

/**
 * statementCursor keeps the opaque cursor only when present.
 * It is never parsed and never built — only passed back.
 */
export function statementCursor(value: string | null): string | undefined {
  if (value === null || value === "") {
    return undefined;
  }
  return value;
}

/**
 * mergeStatementEntries keeps one ledger page honest across reloads
 * and late answers: entries the owner already holds keep their
 * place, newcomers append once, and duplicates collapse on the
 * stable transaction identifier.
 */
export function mergeStatementEntries(
  existing: readonly WalletStatementEntry[],
  incoming: readonly WalletStatementEntry[],
): readonly WalletStatementEntry[] {
  const seen = new Set(existing.map((entry) => entry.transaction_id));
  const merged: WalletStatementEntry[] = [...existing];
  for (const entry of incoming) {
    if (!seen.has(entry.transaction_id)) {
      seen.add(entry.transaction_id);
      merged.push(entry);
    }
  }
  return merged;
}

/**
 * walletFailure translates a refusal by the server code the backend
 * really emits. The wallet adapter answers `invalid_cursor` for a
 * forged cursor, `invalid_limit` for a non-numeric page size and
 * `unauthorized` without a session; anything else falls back to
 * the generic sentence instead of inventing a meaning.
 */
export function walletFailure(translator: Translator, serverCode: string): string {
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
