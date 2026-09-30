/**
 * DOM-free decisions of the accessible UI primitives (P18-T04).
 *
 * The custom elements are thin adapters: they adopt or create nodes, apply the
 * attributes computed here and nothing else. Keeping the decisions pure means
 * the accessibility wiring — accessible names, `aria-describedby` order,
 * invalid/required state, live-region politeness, auto-dismiss policy and
 * focus return — is verified by the Node test runner without a browser.
 *
 * Text never comes from this module: every user-visible string is supplied by
 * the page from the typed catalogs (I18N_STANDARD.md section 2). The
 * primitives compose nothing and translate nothing.
 */

/** HTML identifiers this project generates are plain and URL-safe. */
const ID_PATTERN = /^[A-Za-z][A-Za-z0-9_.:-]*$/;

/** Suffixes of the nodes a field owns, derived from its name. */
const CONTROL_SUFFIX = "-control";
const HINT_SUFFIX = "-hint";
const ERROR_SUFFIX = "-error";

/** Value of a nullable text input, trimmed. */
function text(value: string | null | undefined): string {
  return (value ?? "").trim();
}

/** Fails loudly instead of rendering an unusable primitive. */
function requireId(value: string, role: string): string {
  if (!ID_PATTERN.test(value)) {
    throw new TypeError(`ga-primitives: ${role} must be a valid HTML identifier, got ${JSON.stringify(value)}`);
  }
  return value;
}

/** One hint or error node rendered under a control. */
export interface PrimitiveMessage {
  readonly id: string;
  readonly kind: "hint" | "error";
  readonly text: string;
  /** Error messages are announced immediately; hints are not announced. */
  readonly alert: boolean;
}

/** Input of the field wiring. */
export interface FieldState {
  /** Identifier base of the field; the control is `<name>-control`. */
  readonly name: string;
  /** Already translated label text; an empty label is a programming error. */
  readonly label: string;
  readonly hint?: string | null;
  readonly error?: string | null;
  readonly required?: boolean;
}

/** Everything the field element applies to the DOM. */
export interface FieldWiring {
  readonly controlId: string;
  readonly hintId: string;
  readonly errorId: string;
  readonly labelText: string;
  /** Attributes of the label element (its `for`). */
  readonly labelAttributes: Readonly<Record<string, string>>;
  /** Attributes of the control, always including the accessible name link. */
  readonly controlAttributes: Readonly<Record<string, string>>;
  readonly messages: readonly PrimitiveMessage[];
}

/**
 * fieldWiring computes the label/control/message wiring of one field.
 *
 * It fails closed: a field without a translated label or with a name that is
 * not a usable identifier throws instead of silently producing a control that
 * no assistive technology can name.
 */
export function fieldWiring(state: FieldState): FieldWiring {
  const name = requireId(state.name.trim(), "field name");
  const labelText = text(state.label);
  if (labelText === "") {
    throw new TypeError("ga-primitives: field label is required");
  }

  const controlId = name + CONTROL_SUFFIX;
  const hintId = name + HINT_SUFFIX;
  const errorId = name + ERROR_SUFFIX;
  const hint = text(state.hint);
  const error = text(state.error);

  const messages: PrimitiveMessage[] = [];
  const describedBy: string[] = [];
  if (hint !== "") {
    messages.push({ id: hintId, kind: "hint", text: hint, alert: false });
    describedBy.push(hintId);
  }
  if (error !== "") {
    messages.push({ id: errorId, kind: "error", text: error, alert: true });
    describedBy.push(errorId);
  }

  const controlAttributes: Record<string, string> = { id: controlId };
  if (describedBy.length > 0) {
    controlAttributes["aria-describedby"] = describedBy.join(" ");
  }
  if (state.required === true) {
    controlAttributes["aria-required"] = "true";
  }
  if (error !== "") {
    controlAttributes["aria-invalid"] = "true";
  }

  return {
    controlId,
    hintId,
    errorId,
    labelText,
    labelAttributes: { for: controlId },
    controlAttributes,
    messages,
  };
}

/** One error of a form, keyed by the field identifier it belongs to. */
export interface FieldError {
  readonly fieldId: string;
  readonly message: string;
}

/** One rendered link of the error summary. */
export interface ErrorSummaryItem {
  readonly fieldId: string;
  readonly message: string;
  readonly href: string;
}

/**
 * errorSummaryItems normalizes the errors a form reports: blank entries are
 * dropped, a field appears once (keeping the first message) and the order the
 * page supplied — form order — is preserved.
 *
 * A field id that cannot be a fragment target is dropped as well. The element
 * refuses to move focus to an unusable target, so offering one would render a
 * link that looks like a path to the error and does nothing; the field keeps
 * its own error paragraph in that case. This is the same refusal the adopted
 * server summary applies through `summaryErrors`.
 */
export function errorSummaryItems(errors: readonly FieldError[]): readonly ErrorSummaryItem[] {
  const seen = new Set<string>();
  const items: ErrorSummaryItem[] = [];
  for (const error of errors) {
    const fieldId = text(error.fieldId);
    const message = text(error.message);
    if (fieldId === "" || message === "") {
      continue;
    }
    if (seen.has(fieldId)) {
      continue;
    }
    if (focusTargetId(`#${fieldId}`) === null) {
      continue;
    }
    seen.add(fieldId);
    items.push({ fieldId, message, href: `#${fieldId}` });
  }
  return items;
}

/**
 * focusTargetId resolves the identifier a summary link points at, or null when
 * the fragment is not a usable target. The element refuses to move focus to an
 * unknown id instead of scrolling somewhere unrelated.
 */
export function focusTargetId(href: string): string | null {
  const value = text(href);
  if (!value.startsWith("#")) {
    return null;
  }
  const identifier = value.slice(1);
  if (identifier === "" || identifier.includes("#") || /\s/.test(identifier)) {
    return null;
  }
  return identifier;
}

/** One link of a summary the server rendered: its fragment and its message. */
export interface SummaryLink {
  readonly href: string;
  readonly message: string;
}

/**
 * summaryErrors reads the links of a server-rendered summary as field errors,
 * so the element can complete what the document already lists instead of
 * wiping it. A link whose fragment is not a usable target is dropped, exactly
 * as `errorSummaryItems` drops a blank error; the order the document wrote is
 * the order a person reads, and it is preserved.
 */
export function summaryErrors(links: readonly SummaryLink[]): readonly FieldError[] {
  const errors: FieldError[] = [];
  for (const link of links) {
    const fieldId = focusTargetId(link.href);
    if (fieldId === null) {
      continue;
    }
    errors.push({ fieldId, message: link.message });
  }
  return errors;
}

/** Input of the busy presentation. */
export interface BusyState {
  readonly busy: boolean;
  /** Already translated status text, announced politely while busy. */
  readonly label?: string | null;
}

/** Everything the busy element applies to the DOM. */
export interface BusyPresentation {
  /** Whether the indicator is shown at all. */
  readonly visible: boolean;
  readonly hostAttributes: Readonly<Record<string, string>>;
  readonly indicatorAttributes: Readonly<Record<string, string>>;
  readonly statusAttributes: Readonly<Record<string, string>>;
  readonly statusText: string;
}

/**
 * busyPresentation marks a region as busy without hiding its content: the
 * indicator itself is decorative (`aria-hidden`) and the optional label is the
 * only text announced, through a polite status region.
 */
export function busyPresentation(state: BusyState): BusyPresentation {
  if (!state.busy) {
    return {
      visible: false,
      hostAttributes: {},
      indicatorAttributes: { "aria-hidden": "true" },
      statusAttributes: {},
      statusText: "",
    };
  }
  return {
    visible: true,
    hostAttributes: { "aria-busy": "true" },
    indicatorAttributes: { "aria-hidden": "true" },
    statusAttributes: { role: "status", "aria-live": "polite", "aria-atomic": "true" },
    statusText: text(state.label),
  };
}

/** Severities the toast primitive supports. */
export type ToastSeverity = "info" | "success" | "warning" | "error";

/** Default auto-dismiss delay of non-error toasts, in milliseconds. */
export const TOAST_DEFAULT_DURATION_MS = 5_000;

/** Politeness of the live region a severity requires. */
export interface LiveRegion {
  readonly role: "status" | "alert";
  readonly ariaLive: "polite" | "assertive";
  readonly ariaAtomic: "true";
}

/**
 * toastLiveRegion keeps interrupting severities assertive and everything else
 * polite, so an informational message never cuts off a screen reader.
 */
export function toastLiveRegion(severity: ToastSeverity): LiveRegion {
  if (severity === "error" || severity === "warning") {
    return { role: "alert", ariaLive: "assertive", ariaAtomic: "true" };
  }
  return { role: "status", ariaLive: "polite", ariaAtomic: "true" };
}

/** Timing decision of one visible toast. */
export interface ToastTiming {
  readonly dismissAfterMs: number | null;
  readonly paused: boolean;
  readonly reason: "sticky" | "severity" | "interaction" | "scheduled";
}

/**
 * toastTiming decides whether a toast may disappear on its own.
 *
 * Errors are never auto-dismissed — the reader sets the pace, not the timer —
 * and a toast the user is reading (focused) or pointing at (hovered) keeps its
 * time. `durationMs <= 0` means the message is sticky by configuration.
 */
export function toastTiming(
  severity: ToastSeverity,
  durationMs: number,
  interaction: { readonly focused: boolean; readonly hovered: boolean },
): ToastTiming {
  if (!Number.isFinite(durationMs) || durationMs <= 0) {
    return { dismissAfterMs: null, paused: false, reason: "sticky" };
  }
  if (severity === "error") {
    return { dismissAfterMs: null, paused: false, reason: "severity" };
  }
  if (interaction.focused || interaction.hovered) {
    return { dismissAfterMs: durationMs, paused: true, reason: "interaction" };
  }
  return { dismissAfterMs: durationMs, paused: false, reason: "scheduled" };
}

/** Input of the focus-return decision. */
export interface FocusReturnState {
  /** Whether focus was inside the primitive while it was visible. */
  readonly hadFocusInside: boolean;
  /** Whether the element focused before is still connected to the document. */
  readonly previousFocusConnected: boolean;
}

/**
 * shouldRestoreFocus moves focus back only when this primitive took it and the
 * previous target still exists: stealing focus back to a removed node would
 * leave the user at the top of the document.
 */
export function shouldRestoreFocus(state: FocusReturnState): boolean {
  return state.hadFocusInside && state.previousFocusConnected;
}
