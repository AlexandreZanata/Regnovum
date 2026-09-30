/**
 * DOM-free decisions of the Arena participation page (P18-T06).
 *
 * The page is a server-rendered document and it works without any script: every
 * form has a real `action`, a real `method` and a real CSRF field, the server
 * validates every submitted value against the same closed vocabulary the page
 * rendered, and every transition answers a document or a redirect. What a script
 * can add, and all this module decides, are the two things a document cannot do
 * by itself:
 *
 *   - the local position choice of a visitor. It stays in this browser and is
 *     never sent anywhere; signing in turns it into a preference the
 *     confirmation form starts from, and the server still receives only what the
 *     person submits.
 *   - the attribution selection of an owner. The form offers the arguments the
 *     page listed, and the browser refuses to tick past the limit the server
 *     declares — as a courtesy, never as the authority: the application enforces
 *     the same limit on the request, which is what the adapter tests prove.
 *
 * Keeping the decisions here means the Node runner verifies them without a
 * browser: the element wiring in `arena.ts` only applies what is computed in
 * this file.
 */
import type { ArenaPositionForm } from "../contracts/generated.js";
import type { Translator } from "../i18n/translator.js";

/** The position vocabulary, taken from the generated contract. */
export type Position = ArenaPositionForm["position"];

/** Every position of the vocabulary, in the order the page renders them. */
export const POSITIONS: readonly Position[] = ["agree", "disagree", "undecided"];

/** The attribute the page puts on the group a local choice belongs to. */
export const CHOICE_GROUP_SELECTOR = "[data-ga-choice-group]";

/** The attribute of one local choice button. */
export const CHOICE_ATTRIBUTE = "data-ga-choice";

/** The attribute carrying the Arena identifier the choice belongs to. */
export const ARENA_ATTRIBUTE = "data-ga-arena";

/** The group of the attribution selection. */
export const ATTRIBUTION_GROUP_SELECTOR = "[data-ga-attribution-group]";

/** The prefix of the storage key. One key per Arena, so two pages never mix. */
const STORAGE_PREFIX = "ga.arena.position.";

/**
 * choiceStorageKey is the address of the local choice of one Arena. The key is
 * scoped by the Arena slug — the stable part of the URL, which every visit
 * carries — and not by the opaque identifier, because the choice has to survive
 * the sign-in navigation that turns it into the first value of the confirmation
 * form, and the signed-in page no longer renders the identifier.
 */
export function choiceStorageKey(arenaSlug: string): string {
  return `${STORAGE_PREFIX}${arenaSlug}`;
}

/**
 * arenaSlugFromPath returns the Arena slug of a participation pathname, or null
 * when the address is not one. The slug is the canonical part of the URL and it
 * does not change with the interface locale (I18N_STANDARD.md section 7), which
 * is what makes it a usable scope for a value that lives in this browser only.
 */
export function arenaSlugFromPath(pathname: string): string | null {
  const segments = pathname.split("/").filter((segment) => segment !== "");
  if (segments.length !== 2 || segments[0] !== "arenas") {
    return null;
  }
  return segments[1] ?? null;
}

/**
 * readLocalChoice accepts a stored value only when it is one of the positions the
 * page renders. Anything else — a value written by an older build, a tampered
 * storage, an empty string — is treated as no choice at all: the module never
 * forwards a value it did not render, and never checks a control the server
 * would refuse.
 */
export function readLocalChoice(stored: string | null): Position | null {
  if (stored === null) {
    return null;
  }
  const trimmed = stored.trim();
  for (const position of POSITIONS) {
    if (trimmed === position) {
      return position;
    }
  }
  return null;
}

/** What one click on a local choice button does. */
export interface LocalChoiceDecision {
  /** Whether the click is a choice at all. */
  readonly accepted: boolean;
  /** The value to store and to reflect in the markup, when accepted. */
  readonly position: Position | null;
}

/** chooseLocalChoice decides the outcome of one click on a local choice. */
export function chooseLocalChoice(value: string): LocalChoiceDecision {
  const position = readLocalChoice(value);
  return { accepted: position !== null, position };
}

/** What one toggle of the attribution selection does. */
export interface AttributionDecision {
  /** Whether the toggle is allowed; a refused one leaves the selection as it was. */
  readonly allowed: boolean;
  /** The selection after the toggle, in the order the page listed it. */
  readonly selected: readonly string[];
}

/**
 * attributionSelection decides whether one toggle fits in the limit the server
 * declared. It follows the order the page listed the options, so the selection it
 * reports is the one a person would read in the document, and it is idempotent:
 * toggling the same argument twice returns the same set.
 *
 * A limit that is not a positive integer is no limit at all: the browser then
 * refuses every tick instead of guessing a bound, and the server keeps being the
 * only authority in any case.
 */
export function attributionSelection(
  selected: readonly string[],
  toggled: string,
  checked: boolean,
  limit: number,
): AttributionDecision {
  if (toggled === "") {
    return { allowed: false, selected: [...selected] };
  }
  if (!checked) {
    return { allowed: true, selected: selected.filter((value) => value !== toggled) };
  }
  if (selected.includes(toggled)) {
    return { allowed: true, selected: [...selected] };
  }
  if (!Number.isSafeInteger(limit) || limit < 1 || selected.length >= limit) {
    return { allowed: false, selected: [...selected] };
  }
  return { allowed: true, selected: [...selected, toggled] };
}

/**
 * attributionLimitMessage is the text of the refusal the browser shows when a
 * tick would pass the limit. It is the message the server renders for the same
 * refusal, read from the same catalog key, so a person who acts with scripts
 * and one who submits without them read the same sentence.
 */
export function attributionLimitMessage(translator: Translator, limit: number): string {
  return translator.translate("arenas.participation.errors.too_many_attributions", { max: limit });
}
