/**
 * DOM-free decisions of the account journey's submission guard (P18-T05).
 *
 * The journey is server-rendered and works without any script: every form has a
 * real `action`, a real `method` and a real CSRF field, and the server answers
 * every submission with a document or a redirect. What a script can add, and all
 * this module decides, is the busy state of a submission that is already on its
 * way — and the refusal of a second one while the first is in flight, which is
 * what keeps an impatient double click from opening two sessions.
 *
 * Keeping the decisions here means the rule is verified by the Node runner
 * without a browser: the element wiring in `auth.ts` only applies what is
 * computed in this file.
 */

/** The custom element that presents the busy state (P18-T04). */
export const BUSY_ELEMENT = "ga-busy";

/** Attributes the guard manages on a region while a submission is in flight. */
export const BUSY_REGION_ATTRIBUTES: readonly string[] = ["aria-busy"];

/**
 * Marker the guard puts on the control it disables, so a page restored from the
 * back-forward cache frees exactly that control: the module keeps no memory of
 * the element, and a control the server disabled for its own reason carries no
 * marker and is never touched.
 */
export const BUSY_SUBMITTER_MARKER = "data-ga-busy-submitter";

/** Attributes the guard manages on the control that started the submission. */
export const BUSY_SUBMITTER_ATTRIBUTES: readonly string[] = [
  "disabled",
  "aria-disabled",
  BUSY_SUBMITTER_MARKER,
];

/** Attribute that switches the `ga-busy` element on. */
export const BUSY_ATTRIBUTE = "busy";

/** Decision of one `submit` event. */
export interface SubmissionStart {
  /** Whether the submission goes on; a second one while busy does not. */
  readonly allow: boolean;
}

/**
 * submissionStart allows the first submission of a form and refuses any attempt
 * made while that one is in flight. It never refuses the first one: a guard that
 * could swallow a submission would be worse than no guard at all.
 */
export function submissionStart(inFlight: boolean): SubmissionStart {
  return { allow: !inFlight };
}

/** Attributes one state applies to the region, the submitter and the busy element. */
export interface BusyAttributes {
  readonly region: Readonly<Record<string, string>>;
  readonly submitter: Readonly<Record<string, string>>;
  readonly busyElement: Readonly<Record<string, string>>;
}

/**
 * busyAttributes marks the region as busy without hiding it: the content stays
 * readable, the submitter is disabled so the same form cannot be sent twice, and
 * the `ga-busy` element is switched on — its label was already translated by the
 * server and is never invented here.
 */
export function busyAttributes(): BusyAttributes {
  return {
    region: { "aria-busy": "true" },
    submitter: { disabled: "", "aria-disabled": "true", [BUSY_SUBMITTER_MARKER]: "" },
    busyElement: { [BUSY_ATTRIBUTE]: "" },
  };
}

/**
 * idleAttributes is the state a page returns to when the browser restores it
 * from its back-forward cache: the submission that was in flight was abandoned
 * by the navigation, so the form must be usable again instead of frozen in a
 * busy state nobody will ever clear. Empty maps mean the managed attributes are
 * removed, never rewritten — that is how a disabled submitter becomes pressable
 * again, and it is why the restore looks for the marker the busy state left.
 */
export function idleAttributes(): BusyAttributes {
  return { region: {}, submitter: {}, busyElement: {} };
}
