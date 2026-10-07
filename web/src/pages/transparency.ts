/**
 * Public transparency document presentation (P55-T04).
 *
 * Anyone reads the versioned platform metrics and follows the
 * localized HTML document — no session, no role, no cache
 * mixing with private data. `documentPresentation` projects
 * one metrics answer for one locale: methodology version,
 * UTC period bounds, the echoed timezone label, the
 * derivation instant rendered by `Intl`, and one row per
 * metric code the server sent — grouped integers under
 * localized labels. `documentLink` builds the canonical
 * address of the server-rendered HTML document with the same
 * allowlisted window: the browser navigates there instead of
 * fetching HTML as data.
 *
 * Two refusals shape this module. A metric code the answer
 * did not carry renders no row: suppressed counts are the
 * server's omission and the page infers nothing — neither a
 * zero nor a label without a value — while an empty report is
 * never fabricated. And the link carries only what the
 * contract declares: unparseable instants and unknown locales
 * are dropped instead of reflected, the derivation stays in
 * UTC whatever label the reader chose, and the locale never
 * leaks into the metrics themselves. Every string comes from
 * the catalog the runtime serves; the component that renders
 * a view translates nothing and formats nothing.
 */
import { formatInstant, formatNumber, instantOf } from "../i18n/formats.js";
import type { TransparencyMetricCounts, TransparencyMetrics } from "../contracts/generated.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";

/** Metric codes the contract declares, in catalog order. */
export const METRIC_CODES: readonly (keyof TransparencyMetricCounts)[] = [
  "eligible_accounts",
  "arenas_published",
  "arenas_closed",
  "arenas_restricted",
  "arenas_removed",
  "arguments_published",
  "arguments_withdrawn",
  "position_changes",
  "attributions_valid",
  "attributions_invalidated",
  "influenced_authors",
  "ink_free_granted",
  "ink_free_expired",
  "ink_free_consumed",
  "ink_purchased_granted",
  "ink_purchased_consumed",
  "ink_refunded",
  "ink_admin_adjusted",
  "passes_purchase_granted",
  "passes_member_granted",
  "passes_consumed",
  "reports_filed",
  "actions_recorded",
  "appeals_filed",
  "appeals_reversed",
];

/** Interface locales the document address accepts. */
const DOCUMENT_LOCALES: readonly string[] = ["pt-BR", "en-US"];

/** One rendered metric row: localized label with its count. */
export interface MetricRow {
  readonly metric: string;
  readonly value: string;
}

/** Everything the document renders for one metrics answer. */
export interface TransparencyDocument {
  readonly pageTitle: string;
  readonly heading: string;
  readonly period: string;
  readonly updated: string;
  readonly methodology: string;
  readonly metricHeader: string;
  readonly valueHeader: string;
  readonly rows: readonly MetricRow[];
}

/**
 * assertSafeCount refuses anything that is not an exact
 * integer count. A value outside the safe range — or a
 * fractional one — throws instead of rendering a rounded
 * metric.
 */
export function assertSafeCount(value: number): number {
  if (!Number.isSafeInteger(value)) {
    throw new TypeError(`metric counts must be a safe integer, received ${String(value)}`);
  }
  return value;
}

/**
 * documentPresentation projects one metrics answer for one
 * locale. Only the codes the server sent render a row; a
 * missing code renders nothing — never an inferred zero.
 */
export function documentPresentation(
  translator: Translator,
  locale: Locale,
  document: TransparencyMetrics,
): TransparencyDocument {
  const rows: MetricRow[] = [];
  for (const code of METRIC_CODES) {
    const value: unknown = document.metrics[code];
    if (typeof value !== "number") {
      continue;
    }
    rows.push({
      metric: translator.translate(`transparency.document.rows.${code}`),
      value: formatNumber(locale, assertSafeCount(value)),
    });
  }
  return {
    pageTitle: translator.translate("transparency.document.page_title"),
    heading: translator.translate("transparency.document.heading"),
    period: translator.translate("transparency.document.period", {
      start: formatInstant(locale, document.period_start, { dateStyle: "medium" }),
      end: formatInstant(locale, document.period_end, { dateStyle: "medium" }),
      timezone: document.timezone,
    }),
    updated: translator.translate("transparency.document.updated", {
      at: formatInstant(locale, document.updated_at, { dateStyle: "medium", timeStyle: "short" }),
      version: formatNumber(locale, document.methodology_version),
    }),
    methodology: translator.translate("transparency.document.methodology"),
    metricHeader: translator.translate("transparency.document.metric"),
    valueHeader: translator.translate("transparency.document.value"),
    rows,
  };
}

/** Window the document link may carry; every field is optional. */
export interface DocumentLinkQuery {
  readonly period_start?: string;
  readonly period_end?: string;
  readonly timezone?: string;
  readonly locale?: string;
}

/**
 * validInstant keeps an RFC 3339 instant only when it parses.
 * Anything else is dropped instead of reflected in the
 * address.
 */
function validInstant(value: string | undefined): string | undefined {
  if (value === undefined || value === "") {
    return undefined;
  }
  try {
    instantOf(value);
  } catch {
    return undefined;
  }
  return value;
}

/**
 * documentLocale keeps the interface locale only inside the
 * supported allowlist. Unknown values are dropped — the
 * server falls back without reflection, and the canonical
 * address holds only what it accepts.
 */
function documentLocale(value: string | undefined): string | undefined {
  if (value === undefined || value === "") {
    return undefined;
  }
  return DOCUMENT_LOCALES.includes(value) ? value : undefined;
}

/**
 * documentLink builds the canonical address of the
 * server-rendered HTML document. Period bounds travel only
 * when they parse, the timezone only when present, the locale
 * only when supported — the canonical address holds what the
 * server accepts, and the default window applies otherwise.
 */
export function documentLink(query?: DocumentLinkQuery): string {
  const params = new URLSearchParams();
  const start = validInstant(query?.period_start);
  const end = validInstant(query?.period_end);
  if (start !== undefined) {
    params.set("period_start", start);
  }
  if (end !== undefined) {
    params.set("period_end", end);
  }
  if (query?.timezone !== undefined && query.timezone !== "") {
    params.set("timezone", query.timezone);
  }
  const locale = documentLocale(query?.locale);
  if (locale !== undefined) {
    params.set("locale", locale);
  }
  const suffix = params.toString();
  return suffix === "" ? "/transparency" : `/transparency?${suffix}`;
}
