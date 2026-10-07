/**
 * DOM-free decisions of the Arena participation page (P18-T06; position
 * and aggregate journeys P53-T01).
 *
 * The page is a server-rendered document and it works without any script: every
 * form has a real `action`, a real `method` and a real CSRF field, the server
 * validates every submitted value against the same closed vocabulary the page
 * rendered, and every transition answers a document or a redirect. What a script
 * can add, and all this module decides, are the things a document cannot do
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
 *   - the translated projection of the position journey: the confirmation
 *     and change forms, the owner's private head and change history, and
 *     the failure sentences for exactly the server codes the backend
 *     emits. The private history and the public aggregate never mix: one
 *     account's projection is unreachable from any other, and a visitor
 *     holds no projection at all.
 *
 * Keeping the decisions here means the Node runner verifies them without a
 * browser: the element wiring in `arena.ts` only applies what is computed in
 * this file.
 */
import { formatInstant } from "../i18n/formats.js";
import type {
  ArenaPositionForm,
  PositionChangeHistory,
  PrivatePosition,
} from "../contracts/generated.js";
import type { Locale } from "../i18n/locale.js";
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

/** positionLabel names one vocabulary value in the locale of the page. */
export function positionLabel(translator: Translator, position: Position): string {
  switch (position) {
    case "agree":
      return translator.translate("arenas.participation.choice.agree");
    case "disagree":
      return translator.translate("arenas.participation.choice.disagree");
    case "undecided":
      return translator.translate("arenas.participation.choice.undecided");
  }
}

/** One rendered choice of a position form: value and label travel together. */
export interface PositionChoice {
  readonly value: Position;
  readonly label: string;
}

/** positionChoices lists the vocabulary in the order the page renders it. */
export function positionChoices(translator: Translator): readonly PositionChoice[] {
  return POSITIONS.map((value) => ({ value, label: positionLabel(translator, value) }));
}

/** Everything the page renders for the first confirmation form. */
export interface PositionConfirmView {
  readonly heading: string;
  readonly intro: string;
  readonly choices: readonly PositionChoice[];
  /**
   * The visitor's local choice, when it is one the page rendered:
   * the form starts from it, the server still receives only what
   * the person submits.
   */
  readonly suggested: Position | null;
  readonly submit: string;
}

/**
 * positionConfirmView projects the initial confirmation: the choice
 * is immutable history once recorded, so a different value later
 * conflicts instead of overwriting. Repeating the same value
 * replays the recorded projection instead of writing again.
 */
export function positionConfirmView(translator: Translator, suggested: Position | null): PositionConfirmView {
  return {
    heading: translator.translate("arenas.participation.position.confirm_heading"),
    intro: translator.translate("arenas.participation.position.confirm_intro"),
    choices: positionChoices(translator),
    suggested,
    submit: translator.translate("arenas.participation.position.confirm_submit"),
  };
}

/** Everything the page renders for the change form. */
export interface PositionChangeView {
  readonly heading: string;
  readonly intro: string;
  /** The current position, named so the person sees what moves. */
  readonly current: string;
  readonly choices: readonly PositionChoice[];
  readonly submit: string;
}

/**
 * positionChangeView projects one later change: the move appends an
 * immutable history row and the projection advances in the same
 * server transaction. Changing to the current position conflicts,
 * and a closed Arena accepts no new change.
 */
export function positionChangeView(translator: Translator, current: Position): PositionChangeView {
  return {
    heading: translator.translate("arenas.participation.position.change_heading"),
    intro: translator.translate("arenas.participation.position.change_intro"),
    current: translator.translate("arenas.participation.position.current", {
      position: positionLabel(translator, current),
    }),
    choices: positionChoices(translator),
    submit: translator.translate("arenas.participation.position.change_submit"),
  };
}

/** Everything the page renders for the owner's own projection. */
export interface MyPositionView {
  readonly initial: string;
  readonly current: string;
  readonly version: number;
  readonly updated: string;
}

/**
 * myPositionView projects the private head of one account: initial
 * and current positions with the version the next change must beat.
 * Another account's projection is unreachable — it reads as not
 * found — and a visitor holds no projection at all.
 */
export function myPositionView(translator: Translator, locale: Locale, position: PrivatePosition): MyPositionView {
  return {
    initial: translator.translate("arenas.participation.position.initial", {
      position: positionLabel(translator, position.initial_position),
    }),
    current: translator.translate("arenas.participation.position.current", {
      position: positionLabel(translator, position.current_position),
    }),
    version: position.version,
    updated: formatInstant(locale, position.updated_at, { dateStyle: "medium", timeStyle: "short" }),
  };
}

/** One recorded move of the private history. */
export interface PositionHistoryRow {
  readonly from: string;
  readonly to: string;
  readonly version: number;
  readonly changed: string;
}

/** What the page renders for the private change history. */
export type PositionHistoryView =
  | { readonly state: "ready"; readonly rows: readonly PositionHistoryRow[] }
  | { readonly state: "empty"; readonly empty: string };

/** The heading of the history section, in the locale of the page. */
export function positionHistoryHeading(translator: Translator): string {
  return translator.translate("arenas.participation.history.heading");
}

/**
 * positionHistoryView projects the owner's change history newest
 * first, exactly as the server answers it: the page never reorders
 * immutable history. Labels are translated, instants formatted, and
 * a history that has not started yet names its empty state.
 */
export function positionHistoryView(
  translator: Translator,
  locale: Locale,
  history: PositionChangeHistory,
): PositionHistoryView {
  if (history.items.length === 0) {
    return { state: "empty", empty: translator.translate("arenas.participation.history.empty") };
  }
  return {
    state: "ready",
    rows: history.items.map((item) => ({
      from: positionLabel(translator, item.from_position),
      to: positionLabel(translator, item.to_position),
      version: item.version,
      changed: formatInstant(locale, item.changed_at, { dateStyle: "medium", timeStyle: "short" }),
    })),
  };
}

/**
 * positionFailureField names the form field a refusal belongs to, so
 * the renderer can focus the error where the person fixes it. Only
 * the position field is ever named; anything else answers null and
 * the error stays at the form.
 */
export function positionFailureField(serverCode: string): "position" | null {
  switch (serverCode) {
    case "position_invalid":
      return "position";
    default:
      return null;
  }
}

/**
 * positionFailure translates a refusal by the server code the backend
 * really emits. Codes the backend never emits are not named here:
 * they fall back to the generic sentence instead of inventing a
 * meaning.
 */
export function positionFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "position_invalid":
      return translator.translate("arenas.participation.errors.invalid_choice");
    case "initial_position_already_set":
      return translator.translate("arenas.participation.errors.immutable_position");
    case "version_conflict":
      return translator.translate("arenas.participation.errors.version_conflict");
    case "position_not_found":
      return translator.translate("arenas.participation.errors.position_missing");
    case "arena_not_open":
      return translator.translate("arenas.participation.errors.arena_closed");
    case "account_not_eligible":
      return translator.translate("arenas.participation.errors.not_eligible");
    case "account_suspended":
      return translator.translate("arenas.participation.errors.suspended");
    case "position_same":
      return translator.translate("arenas.participation.errors.same_position");
    case "arena_not_found":
      return translator.translate("arenas.document.not_found.detail");
    default:
      return translator.translate("arenas.participation.errors.generic");
  }
}
