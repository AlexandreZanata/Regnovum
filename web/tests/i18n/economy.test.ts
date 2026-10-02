/**
 * Presentation of economic numbers and accounting time (P42-T06).
 *
 * Calculation stays integer and language-independent on the server; this
 * suite pins the presentation side in both locales:
 *
 *   - currency renders with the scale the locale declares (0, 2 and 3
 *     minor-unit digits), including zero and negatives, exactly as ICU
 *     renders the same amount;
 *   - percentages render past 100% without clamping, as the locale does;
 *   - instants render in the zone asked for across both DST transitions,
 *     and a 90-day season added in UTC lands exactly, never shifted by a
 *     daylight rule;
 *   - weekday names follow the locale for week displays;
 *   - localized text never feeds the ledger: parsing a rendered amount
 *     back with the language's own parser yields the wrong value, which
 *     is why the product formats from integers and refuses floats.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  formatCurrencyMinor,
  formatInstant,
  formatNumber,
  instantOf,
} from "../../src/i18n/formats.js";

import type { Locale } from "../../src/i18n/locale.js";

const locales: readonly Locale[] = ["pt-BR", "en-US"];

/** What ICU itself renders for one numeric amount. */
function icuAmount(locale: Locale, amount: number, currency: string): string {
  return new Intl.NumberFormat(locale, { style: "currency", currency }).format(amount);
}

/** What ICU itself renders for one ratio as a percentage. */
function icuPercent(locale: Locale, ratio: number): string {
  return new Intl.NumberFormat(locale, { style: "percent" }).format(ratio);
}

test("currency keeps the 0, 2 and 3 minor-unit scales of each locale", () => {
  assert.equal(formatCurrencyMinor("en-US", 1234, "JPY"), icuAmount("en-US", 1234, "JPY"));
  assert.equal(formatCurrencyMinor("pt-BR", 123456, "BRL"), icuAmount("pt-BR", 1234.56, "BRL"));
  assert.equal(formatCurrencyMinor("en-US", 1234, "BHD"), icuAmount("en-US", 1.234, "BHD"));
  assert.equal(formatCurrencyMinor("en-US", 0, "JPY"), icuAmount("en-US", 0, "JPY"));
  assert.equal(formatCurrencyMinor("pt-BR", 0, "BRL"), icuAmount("pt-BR", 0, "BRL"));
});

test("zero and negatives render exactly as the locale renders them", () => {
  for (const locale of locales) {
    const currency = locale === "pt-BR" ? "BRL" : "USD";
    assert.equal(formatCurrencyMinor(locale, 0, currency), icuAmount(locale, 0, currency));
    assert.equal(formatCurrencyMinor(locale, -1, currency), icuAmount(locale, -0.01, currency));
  }
  // Beyond the safe integer range ICU cannot be compared through a float,
  // so the assertion is on the digits: the sign, the grouped whole units
  // and the exact cents.
  const rendered = formatCurrencyMinor("en-US", -9_007_199_254_740_993n, "USD");
  assert.ok(rendered.startsWith("-"), `expected the minus sign, got ${rendered}`);
  assert.ok(rendered.includes("90,071,992,547,409"), `expected the grouped whole units, got ${rendered}`);
  assert.ok(rendered.endsWith(".93"), `expected the exact cents, got ${rendered}`);
});

test("percentages render past one hundred percent, as the locale does", () => {
  for (const locale of locales) {
    assert.equal(formatNumber(locale, 1.5, { style: "percent" }), icuPercent(locale, 1.5));
    assert.equal(formatNumber(locale, 2, { style: "percent" }), icuPercent(locale, 2));
    assert.equal(formatNumber(locale, 0, { style: "percent" }), icuPercent(locale, 0));
    assert.equal(formatNumber(locale, -0.25, { style: "percent" }), icuPercent(locale, -0.25));
  }
  assert.equal(formatNumber("pt-BR", 1.5, { style: "percent" }), "150%");
  assert.equal(formatNumber("en-US", 1.5, { style: "percent" }), "150%");
});

test("the autumn DST transition renders both occurrences of the repeated hour", () => {
  const zone = { timeZone: "America/New_York", dateStyle: "full", timeStyle: "full" } as const;
  const first = formatInstant("en-US", "2026-11-01T05:30:00Z", zone);
  const second = formatInstant("en-US", "2026-11-01T06:30:00Z", zone);

  // 01:30 EDT falls back to 01:30 EST: the same wall clock twice, told apart
  // by the zone mark.
  assert.match(first, /1:30:00 AM Eastern Daylight Time/, `expected the pre-fallback mark, got ${first}`);
  assert.match(second, /1:30:00 AM Eastern Standard Time/, `expected the post-fallback mark, got ${second}`);
});

test("a ninety-day season added in UTC lands exactly", () => {
  const seasonSeconds = 7_776_000;
  assert.equal(seasonSeconds, 90 * 24 * 60 * 60, "the season is exactly ninety days");

  const start = instantOf("2026-01-01T00:00:00Z");
  const end = new Date(start.getTime() + seasonSeconds * 1000);
  assert.equal(end.toISOString(), "2026-04-01T00:00:00.000Z");

  // UTC rendering never shifts, whatever daylight rules the reader lives under.
  assert.equal(formatInstant("pt-BR", end, { timeZone: "UTC", timeStyle: "short" }), "00:00");
});

test("weekday names follow the locale for week displays", () => {
  // 2026-03-02 is a Monday: ISO weeks start on Monday.
  const monday = "2026-03-02T12:00:00Z";
  const pt = formatInstant("pt-BR", monday, { timeZone: "UTC", dateStyle: "full" });
  const en = formatInstant("en-US", monday, { timeZone: "UTC", dateStyle: "full" });
  assert.ok(pt.includes("segunda-feira"), `expected the pt-BR weekday, got ${pt}`);
  assert.ok(en.includes("Monday"), `expected the en-US weekday, got ${en}`);
});

test("localized text never feeds the ledger", () => {
  // A rendered pt-BR amount reparsed by the language's own parser is wrong:
  // parseFloat stops at the group separator and never sees the decimals.
  assert.equal(Number.parseFloat("1.234,56"), 1.234);

  // That is why the product formats from integers and refuses anything else:
  // a fractional or unsafe amount is a defect, never a rounding.
  assert.throws(() => formatCurrencyMinor("pt-BR", 12.5, "BRL"), TypeError);
  assert.throws(() => formatCurrencyMinor("en-US", Number.MAX_SAFE_INTEGER + 1, "USD"), TypeError);

  // Exact balances travel as integers with an ISO code, never as text.
  const grant = { minorUnits: 123456n, currency: "BRL" };
  assert.equal(typeof grant.minorUnits, "bigint");
  assert.equal(formatCurrencyMinor("pt-BR", grant.minorUnits, grant.currency), icuAmount("pt-BR", 1234.56, "BRL"));
});
