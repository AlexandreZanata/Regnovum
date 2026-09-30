/**
 * Entry module of the account journey (P18-T05).
 *
 * It is loaded as a native ES module by every page of the journey, and it is an
 * enhancer and nothing else. The forms already submit without it, the server
 * already validates every field, and the errors are already rendered — so
 * removing this file from the page changes nothing about what the product
 * accepts or what a person can accomplish. What it adds is the two things a
 * document alone cannot do:
 *
 *   - the `ga-busy` state of a submission in flight, whose label the server
 *     already translated into the markup (the primitive announces it through a
 *     polite status region);
 *   - the refusal of a second submission while the first is on its way, so an
 *     impatient double click does not open two sessions.
 *
 * Registration of the primitives happens here rather than in an inline script
 * because the browser policy of this binary refuses inline code
 * (`script-src 'self'`, no nonce): the module is the only script a page of the
 * journey loads.
 */
import { registerPrimitives } from "../components/primitives/index.js";
import {
  BUSY_ATTRIBUTE,
  BUSY_ELEMENT,
  BUSY_REGION_ATTRIBUTES,
  BUSY_SUBMITTER_ATTRIBUTES,
  BUSY_SUBMITTER_MARKER,
  busyAttributes,
  idleAttributes,
  submissionStart,
} from "./submission.js";
import type { BusyAttributes } from "./submission.js";

/** Whether a submission of this page is already in flight. */
let inFlight = false;

/**
 * Attribute marking a form whose submission is already guarded. Installation is
 * idempotent per form because two modules of the same page may both ask for it —
 * the account journey and the Arena participation page share this file — and a
 * submission must not be guarded twice by accident.
 */
const GUARDED_ATTRIBUTE = "data-ga-guarded";

/** Whether the back-forward cache listener of this document is installed. */
let pageShowInstalled = false;

/** Applies one set of managed attributes to one element, clearing the rest. */
function applyManaged(
  element: Element,
  managed: readonly string[],
  attributes: Readonly<Record<string, string>>,
): void {
  for (const name of managed) {
    element.removeAttribute(name);
  }
  for (const name of Object.keys(attributes)) {
    const value = attributes[name];
    if (value !== undefined) {
      element.setAttribute(name, value);
    }
  }
}

/** Applies one busy or idle state to the form, its submitter and its busy element. */
function applyState(form: HTMLFormElement, submitter: Element | null, state: BusyAttributes): void {
  applyManaged(form, BUSY_REGION_ATTRIBUTES, state.region);
  if (submitter !== null) {
    applyManaged(submitter, BUSY_SUBMITTER_ATTRIBUTES, state.submitter);
  }
  const busy = form.querySelector(BUSY_ELEMENT);
  if (busy !== null) {
    applyManaged(busy, [BUSY_ATTRIBUTE], state.busyElement);
  }
}

/** Resolves the control a person pressed, including the implicit submission. */
function submitterOf(event: Event, form: HTMLFormElement): Element | null {
  if (event instanceof SubmitEvent && event.submitter !== null) {
    return event.submitter;
  }
  return form.querySelector("button[type=submit], input[type=submit], button:not([type])");
}

/** Installs the guard on one form of the journey. */
function guard(form: HTMLFormElement): void {
  if (form.hasAttribute(GUARDED_ATTRIBUTE)) {
    return;
  }
  form.setAttribute(GUARDED_ATTRIBUTE, "");
  form.addEventListener("submit", (event: Event): void => {
    const decision = submissionStart(inFlight);
    if (!decision.allow) {
      event.preventDefault();
      return;
    }
    inFlight = true;
    applyState(form, submitterOf(event, form), busyAttributes());
  });
}

/** Installs the guard on every form of the page and clears it on a restore. */
export function installSubmissionGuard(document: Document = globalThis.document): void {
  for (const form of document.querySelectorAll("form")) {
    if (form instanceof HTMLFormElement) {
      guard(form);
    }
  }

  // A page restored from the back-forward cache comes back while its submission
  // is still recorded as in flight: the navigation abandoned it, so the form has
  // to be usable again.
  if (pageShowInstalled) {
    return;
  }
  pageShowInstalled = true;
  globalThis.addEventListener("pageshow", (event: PageTransitionEvent): void => {
    if (!event.persisted) {
      return;
    }
    inFlight = false;
    const idle = idleAttributes();
    for (const form of document.querySelectorAll("form")) {
      if (!(form instanceof HTMLFormElement)) {
        continue;
      }
      applyState(form, null, idle);
      // The guard disabled the control that started the abandoned submission,
      // and the restored document still carries that disabled state: freeing
      // the marked control is what makes the form usable again. A control the
      // server disabled for its own reason carries no marker.
      for (const submitter of form.querySelectorAll(`[${BUSY_SUBMITTER_MARKER}]`)) {
        applyManaged(submitter, BUSY_SUBMITTER_ATTRIBUTES, idle.submitter);
      }
    }
  });
}

registerPrimitives();
installSubmissionGuard();
