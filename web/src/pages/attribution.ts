/**
 * Influence attribution presentation (P53-T05).
 *
 * One position change credits the eligible arguments that moved it:
 * the JSON call (`recordAttributions`) carries the identifiers the
 * page offered, and the server-rendered document
 * (`submitArenaAttributions`) carries the same selection through its
 * form. What lives in this module, DOM-free so the Node runner
 * verifies it without a browser, are the three things such a
 * renderer needs: the eligibility filter that keeps the selection
 * inside the listed set, the translated view of the public counts,
 * and the failure sentences for exactly the server codes the
 * backend emits.
 *
 * Nothing here scores anyone: the public document carries counts
 * only — valid credits and crediting people, each person counted
 * once — and no attributor identity ever serializes. The
 * administrative signals stay on the moderator surface (P55). After
 * a commit the page re-reads: the recorded set the server answers
 * is what renders, and a replay resolves it without writing again.
 * An empty selection is a valid skip, never a fake attribution.
 */
import { formatInstant, formatNumber } from "../i18n/formats.js";
import type { ArgumentAttributionMetrics } from "../contracts/generated.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";

/**
 * eligibleArguments keeps the identifiers the page offered. Anything
 * outside the eligible set is dropped instead of sent to fail, in
 * the order the page listed it — so a foreign identifier can never
 * become a request, and duplicates collapse to one credit.
 */
export function eligibleArguments(
  selected: readonly string[],
  eligible: readonly string[],
): readonly string[] {
  const allowed = new Set(eligible);
  const kept: string[] = [];
  for (const id of selected) {
    if (id !== "" && allowed.has(id) && !kept.includes(id)) {
      kept.push(id);
    }
  }
  return kept;
}

/** Everything the page renders for the public counts of one argument. */
export interface InfluenceCountsView {
  readonly valid: string;
  readonly people: string;
  readonly checked: string;
}

/**
 * influenceCountsView projects one metrics answer for one locale.
 * Counts are formatted, the instant rendered — and nothing else
 * travels: there is no handle, no list of people, no scoreboard.
 */
export function influenceCountsView(
  translator: Translator,
  locale: Locale,
  metrics: ArgumentAttributionMetrics,
): InfluenceCountsView {
  return {
    valid: translator.translate("arenas.arguments.influence_valid", {
      count: formatNumber(locale, metrics.valid_attributions),
    }),
    people: translator.translate("arenas.arguments.influence_people", {
      count: formatNumber(locale, metrics.distinct_people),
    }),
    checked: formatInstant(locale, metrics.checked_at, { dateStyle: "medium", timeStyle: "short" }),
  };
}

/**
 * attributionFailure translates a refusal by the server code the
 * backend really emits. The change and argument lookups reuse the
 * sentences the debate surface already owns; only the attribution
 * refusals live here. Anything else falls back to the generic
 * sentence instead of inventing a meaning.
 */
export function attributionFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "persuasion_self_attribution":
      return translator.translate("arenas.arguments.failure_self");
    case "persuasion_duplicate_attribution":
      return translator.translate("arenas.arguments.failure_duplicate");
    case "persuasion_cross_arena_argument":
      return translator.translate("arenas.arguments.failure_cross_arena");
    case "persuasion_argument_not_before_change":
    case "persuasion_argument_not_eligible":
      return translator.translate("arenas.arguments.failure_ineligible");
    case "persuasion_too_many_attributions":
      return translator.translate("arenas.arguments.failure_too_many");
    case "change_not_found":
      return translator.translate("arenas.participation.errors.change_not_found");
    case "argument_not_found":
      return translator.translate("arenas.arguments.failure_missing");
    default:
      return translator.translate("arenas.arguments.failure_generic");
  }
}
