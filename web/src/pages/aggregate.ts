/**
 * Aggregate reveal of the Arena participation page (P18-T06 journey,
 * "Visitante entende uma Arena").
 *
 * The page is a server-rendered document and the aggregate already has a
 * server-side path — the reveal link points at `?reveal=1`, which renders it —
 * so what the module adds is only immediacy: the visitor's local choice asks
 * the public API and the result appears in place.
 *
 * What is decided here, without a DOM, is the translated view one rendering
 * needs. Every string comes from the catalog the runtime serves, the counts are
 * formatted for the resolved locale, and the derivation instant is rendered by
 * `Intl` instead of being shown as the RFC 3339 string the contract carries.
 * The component that renders the view never reads a catalog or formats
 * anything.
 */
import { aggregateView } from "../components/position-aggregate/model.js";
import { formatInstant, formatNumber } from "../i18n/formats.js";
import type { AggregateText, AggregateView } from "../components/position-aggregate/model.js";
import type { PositionAggregate } from "../contracts/generated.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";

/** aggregateText reads the catalog and formats the values of one rendering. */
export function aggregateText(translator: Translator, locale: Locale, aggregate: PositionAggregate): AggregateText {
  return {
    heading: translator.translate("arenas.participation.aggregate.heading"),
    total: translator.translate("arenas.participation.aggregate.total", { total: aggregate.participants_total }),
    currentHeading: translator.translate("arenas.participation.aggregate.current"),
    initialHeading: translator.translate("arenas.participation.aggregate.initial"),
    checked: translator.translate("arenas.participation.aggregate.checked", {
      instant: formatInstant(locale, aggregate.checked_at, { dateStyle: "medium", timeStyle: "short" }),
    }),
    suppressedNote: translator.translate("arenas.participation.aggregate.suppressed"),
    choices: {
      agree: translator.translate("arenas.participation.choice.agree"),
      disagree: translator.translate("arenas.participation.choice.disagree"),
      undecided: translator.translate("arenas.participation.choice.undecided"),
    },
    formatCount: (value: number): string => formatNumber(locale, value),
  };
}

/**
 * aggregatePresentation is the view of one aggregate: the catalog text of the
 * resolved locale plus the contract value. The page hands the result to
 * `ga-position-aggregate`, which renders it.
 */
export function aggregatePresentation(
  translator: Translator,
  locale: Locale,
  aggregate: PositionAggregate,
): AggregateView {
  return aggregateView(aggregate, aggregateText(translator, locale, aggregate));
}
