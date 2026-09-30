/**
 * Entry module of the Arena participation page (P18-T06).
 *
 * It is loaded as a native ES module by the page and it is an enhancer and
 * nothing else. Every transition already works without it — the server renders
 * the forms, validates every value against the same closed vocabulary, refuses
 * what it must and answers a document or a redirect — so removing this file
 * changes nothing about what the product accepts. What it adds is what a
 * document cannot do on its own:
 *
 *   - the local position choice of a visitor, kept in this browser and never
 *     sent anywhere, scoped by the Arena slug so it survives the sign-in
 *     navigation and starts the confirmation form from what the person picked;
 *   - the refusal of a submission already in flight, so an impatient double
 *     click does not publish twice (the attempt key of the publication form is
 *     the server-side guarantee; this is the courtesy);
 *   - the refusal to tick past the attribution limit the page declares;
 *   - the reveal of the public aggregate as soon as the visitor picks a
 *     position, through the public positions contract (the reveal link keeps
 *     its server-side path as the resilience of the enhancement);
 *   - the locale rendering of the instants the server wrote as machine values,
 *     so a person reads a date while `<time datetime>` keeps the RFC 3339;
 *   - the sentence of a refused attribution tick, so the courtesy refusal
 *     explains itself instead of only marking the group invalid.
 *
 * Registration of the primitives and the submission guard come from the account
 * journey's module: the two surfaces share the same document conventions
 * (`ga-busy`, `ga-error-summary`, `ga-toast`), and the browser policy of this
 * binary refuses inline code, so the module is the only script the page loads.
 */
import {
  GaPositionAggregateElement,
  POSITION_AGGREGATE_TAG,
  definePositionAggregate,
} from "../components/position-aggregate/position-aggregate.js";
import { createPositionsClient } from "../core/clients/positions.js";
import { createHttpCore } from "../core/http.js";
import { resolveLocale } from "../i18n/locale.js";
import { createTranslator } from "../i18n/translator.js";
import { aggregatePresentation } from "./aggregate.js";
import { installSubmissionGuard } from "./auth.js";
import { timeText } from "./instants.js";
import {
  ARENA_ATTRIBUTE,
  ATTRIBUTION_GROUP_SELECTOR,
  CHOICE_ATTRIBUTE,
  CHOICE_GROUP_SELECTOR,
  arenaSlugFromPath,
  attributionLimitMessage,
  attributionSelection,
  choiceStorageKey,
  chooseLocalChoice,
  readLocalChoice,
} from "./participation.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";
import type { Position } from "./participation.js";

/** The catalog namespaces every text this page renders lives in. */
const PAGE_NAMESPACES: readonly ["arenas"] = ["arenas"];

/** The storage the page may use, or null when the browser refuses it. */
function storageOf(): Storage | null {
  try {
    const storage = globalThis.localStorage;
    // A browser configured to refuse storage throws on access, and a page
    // that renders either way must not break because of it.
    const probe = "__ga_probe__";
    storage.setItem(probe, "1");
    storage.removeItem(probe);
    return storage;
  } catch {
    return null;
  }
}

/** The Arena the page belongs to, or the empty string when it declares none. */
function arenaOf(document: Document): string {
  const group = document.querySelector(CHOICE_GROUP_SELECTOR);
  if (group === null) {
    return "";
  }
  return (group.getAttribute(ARENA_ATTRIBUTE) ?? "").trim();
}

/** Marks one choice button as the chosen one, and every other as unchosen. */
function applyChoice(group: Element, position: Position | null): void {
  for (const button of group.querySelectorAll(`[${CHOICE_ATTRIBUTE}]`)) {
    const value = (button.getAttribute(CHOICE_ATTRIBUTE) ?? "").trim();
    if (position !== null && value === position) {
      button.setAttribute("aria-pressed", "true");
    } else {
      button.setAttribute("aria-pressed", "false");
    }
  }
}

/**
 * preselectPosition checks the stored choice in the position form, and only when
 * the server did not check one already: the stored projection of an owner is the
 * server's answer and outranks anything this browser remembers.
 */
function preselectPosition(document: Document, position: Position | null): void {
  if (position === null) {
    return;
  }
  const form = document.querySelector('form input[name="position"]');
  if (form === null) {
    return;
  }
  const group = form.closest("fieldset");
  if (group === null) {
    return;
  }
  for (const radio of group.querySelectorAll('input[name="position"]')) {
    if (radio instanceof HTMLInputElement && radio.checked) {
      return;
    }
  }
  const target = group.querySelector(`input[name="position"][value="${position}"]`);
  if (target instanceof HTMLInputElement) {
    target.checked = true;
  }
}

/**
 * Installs the local choice of a visitor and the preselection of the form.
 * `onChosen` runs after an accepted choice, and only then: a stored choice
 * applied on load is not a person choosing, so it never reveals anything.
 *
 * The scope is the Arena slug the URL carries, not the identifier the anonymous
 * block renders: the signed-in page no longer renders the identifier, and the
 * choice has to survive the sign-in navigation to start the confirmation form.
 */
function installLocalChoice(document: Document, arenaSlug: string, onChosen: () => void): void {
  const storage = storageOf();
  const stored = storage === null || arenaSlug === "" ? null : readLocalChoice(storage.getItem(choiceStorageKey(arenaSlug)));

  const group = document.querySelector(CHOICE_GROUP_SELECTOR);
  if (group !== null) {
    applyChoice(group, stored);
    group.addEventListener("click", (event: Event): void => {
      const target = event.target;
      if (!(target instanceof Element)) {
        return;
      }
      const button = target.closest(`[${CHOICE_ATTRIBUTE}]`);
      if (button === null || !group.contains(button)) {
        return;
      }
      const decision = chooseLocalChoice(button.getAttribute(CHOICE_ATTRIBUTE) ?? "");
      if (!decision.accepted || decision.position === null) {
        return;
      }
      if (storage !== null && arenaSlug !== "") {
        storage.setItem(choiceStorageKey(arenaSlug), decision.position);
      }
      applyChoice(group, decision.position);
      onChosen();
    });
  }

  preselectPosition(document, stored);

  // The confirmation is the moment the local choice stops being local: the
  // server recorded it, and a stale copy in this browser must not survive it.
  // Both position transitions count: the confirmation records the first
  // position and a change records a later one.
  for (const form of document.querySelectorAll('form[action*="/position"]')) {
    form.addEventListener("submit", (): void => {
      if (storage !== null && arenaSlug !== "") {
        storage.removeItem(choiceStorageKey(arenaSlug));
      }
    });
  }
}

/** Marks the refusal message the browser renders, so it is never duplicated. */
const ATTRIBUTION_MESSAGE_ATTRIBUTE = "data-ga-attribution-message";

/**
 * installAttributionLimit refuses the tick that would pass the limit the page
 * declares. It never unchecks a selection the person made within the limit, and
 * it marks the group as invalid so the refusal is visible instead of silent.
 *
 * The refusal also shows the same sentence the server renders for it, from the
 * same catalog key: a person who ticks past the limit with scripts and one who
 * submits without them read the same message. When the server already rendered
 * it for the last submission, the browser does not repeat it.
 */
function installAttributionLimit(document: Document, limit: number, translator: Translator): void {
  const group = document.querySelector(ATTRIBUTION_GROUP_SELECTOR);
  if (group === null || limit < 1) {
    return;
  }

  const clearMessage = (): void => {
    group.querySelector(`[${ATTRIBUTION_MESSAGE_ATTRIBUTE}]`)?.remove();
  };

  const showMessage = (): void => {
    const serverMessage = group.querySelector(`[role="alert"]:not([${ATTRIBUTION_MESSAGE_ATTRIBUTE}])`);
    if (serverMessage !== null) {
      return;
    }
    let message = group.querySelector(`[${ATTRIBUTION_MESSAGE_ATTRIBUTE}]`);
    if (!(message instanceof HTMLParagraphElement)) {
      message = document.createElement("p");
      message.setAttribute(ATTRIBUTION_MESSAGE_ATTRIBUTE, "");
      message.setAttribute("role", "alert");
      group.append(message);
    }
    message.textContent = attributionLimitMessage(translator, limit);
  };

  group.addEventListener("change", (event: Event): void => {
    const target = event.target;
    if (!(target instanceof HTMLInputElement) || target.type !== "checkbox") {
      return;
    }
    // The selection as it was before this toggle, in the order the page
    // listed it: the decision is about adding one argument to it.
    const previous: string[] = [];
    for (const checkbox of group.querySelectorAll('input[type="checkbox"]')) {
      if (checkbox instanceof HTMLInputElement && checkbox.checked && checkbox !== target) {
        previous.push(checkbox.value);
      }
    }
    const decision = attributionSelection(previous, target.value, target.checked, limit);
    if (!decision.allowed) {
      target.checked = false;
      group.setAttribute("aria-invalid", "true");
      showMessage();
      return;
    }
    group.removeAttribute("aria-invalid");
    clearMessage();
  });
}

/** The limit the page declares for one attribution, or zero when it declares none. */
function attributionLimit(document: Document): number {
  const group = document.querySelector(ATTRIBUTION_GROUP_SELECTOR);
  if (group === null) {
    return 0;
  }
  const declared = Number.parseInt(group.getAttribute("data-ga-attribution-limit") ?? "", 10);
  return Number.isFinite(declared) ? declared : 0;
}

/**
 * installLocalizedInstants renders the machine instants the server wrote into
 * the document in the locale the page was rendered in. The `datetime`
 * attribute keeps the machine value; only the text a person reads changes, and
 * an element the page cannot safely rewrite is left exactly as it was.
 */
function installLocalizedInstants(document: Document, locale: Locale): void {
  for (const element of document.querySelectorAll("time[datetime]")) {
    const datetime = element.getAttribute("datetime") ?? "";
    const text = timeText(locale, datetime, element.textContent ?? "");
    if (text !== null) {
      element.textContent = text;
    }
  }
}

/** The query the server-rendered reveal link carries. */
const REVEAL_PARAM = "reveal";
const REVEAL_VALUE = "1";

/** Attribute that marks the aggregate section while the read is in flight. */
const REVEAL_BUSY_ATTRIBUTE = "aria-busy";

/**
 * revealLinkOf finds the server-rendered reveal link: the same-origin anchor
 * whose query asks the server for the aggregate. It is also the marker that the
 * aggregate is not on the page yet — when it is absent, the page has already
 * revealed the result (or has none) and the module stays out.
 */
function revealLinkOf(document: Document): HTMLAnchorElement | null {
  const origin = new URL(document.baseURI).origin;
  for (const anchor of document.querySelectorAll("a[href]")) {
    if (!(anchor instanceof HTMLAnchorElement)) {
      continue;
    }
    let url: URL;
    try {
      url = new URL(anchor.getAttribute("href") ?? "", document.baseURI);
    } catch {
      continue;
    }
    if (url.origin === origin && url.searchParams.get(REVEAL_PARAM) === REVEAL_VALUE) {
      return anchor;
    }
  }
  return null;
}

/**
 * installAggregateReveal makes the reveal immediate for the visitor.
 *
 * The public aggregate is not a secret — the contract of the endpoint says the
 * local choice gates the UI, never the API — so the module reads it through the
 * positions client as soon as a position is chosen, and it also takes over the
 * server-rendered reveal link. A failed read never loses the journey: a choice
 * leaves the server-rendered section untouched, and the link navigates to the
 * server-rendered aggregate.
 *
 * It returns the callback the local choice calls, or null when the page offers
 * no visitor block to take the Arena identifier from (a signed-in page), in
 * which case the reveal stays a navigation. Without scripts none of this runs.
 */
function installAggregateReveal(
  document: Document,
  arenaID: string,
  locale: Locale,
  translator: Translator,
): (() => void) | null {
  const link = revealLinkOf(document);
  const section = link?.closest("section") ?? null;
  if (link === null || section === null || arenaID === "") {
    return null;
  }

  const positions = createPositionsClient(createHttpCore());
  let revealing = false;
  let revealed = false;

  /**
   * reveal reads the public aggregate and renders it in place.
   *
   * `fallbackToServer` is the difference between the two callers: a person who
   * pressed the reveal link must end up seeing the aggregate even when the read
   * fails, so the browser follows the link the server rendered; a person who
   * only picked a position loses nothing, because the section keeps the link
   * exactly where it was.
   */
  const reveal = async (fallbackToServer: boolean): Promise<void> => {
    if (revealing || revealed) {
      return;
    }
    revealing = true;
    section.setAttribute(REVEAL_BUSY_ATTRIBUTE, "true");
    try {
      const aggregate = await positions.aggregate(arenaID);
      const element = document.createElement(POSITION_AGGREGATE_TAG);
      if (!(element instanceof GaPositionAggregateElement)) {
        // A registry that refused the definition leaves a plain element; the
        // page keeps the server-rendered section instead of writing into a node
        // that has no view.
        return;
      }
      element.view = aggregatePresentation(translator, locale, aggregate);
      section.replaceChildren(element);
      revealed = true;
      element.focusHeading();
    } catch {
      if (fallbackToServer) {
        globalThis.location.assign(link.href);
        return;
      }
      // A choice that cannot reveal changes nothing: the section stays as the
      // server rendered it and the reveal link remains the way forward.
    } finally {
      section.removeAttribute(REVEAL_BUSY_ATTRIBUTE);
      revealing = false;
    }
  };

  link.addEventListener("click", (event: Event): void => {
    event.preventDefault();
    void reveal(true);
  });

  return (): void => {
    void reveal(false);
  };
}

/** Installs the whole enhancement on one document. */
export function installArenaPage(document: Document = globalThis.document): void {
  definePositionAggregate();
  installSubmissionGuard(document);
  // The document arrives rendered in a locale the server resolved, and the
  // browser only reads it: anything the module renders afterwards uses it.
  const locale = resolveLocale([document.documentElement.lang]);
  const translator = createTranslator(locale, { namespaces: PAGE_NAMESPACES });
  installLocalizedInstants(document, locale);
  // The local choice is scoped by the slug the URL carries, present on every
  // visit; the aggregate read needs the opaque identifier, which only the
  // visitor block renders.
  const arenaSlug = arenaSlugFromPath(new URL(document.baseURI).pathname) ?? "";
  const arenaID = arenaOf(document);
  const reveal = installAggregateReveal(document, arenaID, locale, translator);
  installLocalChoice(document, arenaSlug, reveal ?? ((): void => undefined));
  installAttributionLimit(document, attributionLimit(document), translator);
}

installArenaPage();
