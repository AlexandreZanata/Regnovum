/**
 * `ga-toast` — accessible transient message (P18-T04).
 *
 * Tag: `ga-toast`.
 *
 * Responsibility: present one transient message — interrupting for problems,
 * waiting its turn for everything else — and then get out of the way.
 *
 * Attributes (observed: a change re-renders the toast and re-arms the
 * dismissal, so an attribute is never a value the element silently ignores):
 *
 *   severity       `info` (default), `success`, `warning` or `error`
 *   duration       auto-dismiss delay in milliseconds; `0` keeps it sticky
 *   dismiss-label  translated accessible name of the dismiss button (required
 *                  for the button to be reachable by assistive technology)
 *
 * Events: none. Dismissal is an effect of the element itself, not an intent
 * another component observes.
 *
 * States: informational (polite, auto-dismissed), problem (assertive, sticky),
 * and dismissed (removed from the document). Rules the primitive enforces:
 * errors and warnings interrupt (`role="alert"`), informational messages wait
 * their turn (`role="status"`); errors never auto-dismiss; a toast that is
 * focused or hovered keeps its time.
 *
 * Keyboard and focus: `Escape` dismisses it; and when the toast had focus,
 * focus returns to wherever the user was before it appeared — never to a node
 * that no longer exists.
 *
 * CSS: `ga-toast`, its `ga-toast--<severity>` variants and its `__dismiss`
 * button, styled by primitives.css with the tokens of tokens.css.
 *
 * External effects: the dismissal timer and the listeners, all cancelled in
 * `disconnectedCallback`, so a removed toast never fires.
 *
 * Usage:
 *   <ga-toast severity="info"><p>Posição confirmada.</p></ga-toast>
 */
import { ensureChild, toggleHidden } from "./dom.js";
import { TOAST_DEFAULT_DURATION_MS, shouldRestoreFocus, toastLiveRegion, toastTiming } from "./model.js";
import type { ToastSeverity } from "./model.js";

const OBSERVED_ATTRIBUTES: readonly string[] = ["severity", "duration", "dismiss-label"];
const SEVERITIES: readonly ToastSeverity[] = ["info", "success", "warning", "error"];
const DISMISS_SELECTOR = "[data-ga-dismiss]";
const DISMISS_CLASS = "ga-toast__dismiss";

export class GaToastElement extends HTMLElement {
  static observedAttributes: readonly string[] = OBSERVED_ATTRIBUTES;

  private timer: number | null = null;

  private hovered = false;

  private hadFocusInside = false;

  private previousFocus: HTMLElement | null = null;

  connectedCallback(): void {
    this.classList.add("ga-toast");
    this.addEventListener("pointerenter", this.onPointerEnter);
    this.addEventListener("pointerleave", this.onPointerLeave);
    this.addEventListener("focusin", this.onFocusChange);
    this.addEventListener("focusout", this.onFocusChange);
    this.addEventListener("keydown", this.onKeyDown);
    this.addEventListener("click", this.onClick);
    // Where the user was, before anything about this toast can move focus.
    this.rememberFocus();
    this.hadFocusInside = this.contains(this.ownerDocument.activeElement);
    this.render();
    this.schedule();
  }

  disconnectedCallback(): void {
    this.clearTimer();
    this.removeEventListener("pointerenter", this.onPointerEnter);
    this.removeEventListener("pointerleave", this.onPointerLeave);
    this.removeEventListener("focusin", this.onFocusChange);
    this.removeEventListener("focusout", this.onFocusChange);
    this.removeEventListener("keydown", this.onKeyDown);
    this.removeEventListener("click", this.onClick);
  }

  /** Re-reads the observed attributes; the upgrade renders them once more. */
  attributeChangedCallback(): void {
    if (!this.isConnected) {
      return;
    }
    this.render();
    this.schedule();
  }

  /** Severity of this toast, defaulting to `info`. */
  get severity(): ToastSeverity {
    const value = (this.getAttribute("severity") ?? "info").trim();
    return SEVERITIES.includes(value as ToastSeverity) ? (value as ToastSeverity) : "info";
  }

  /** Remembers where the user was, for focus return after dismissal. */
  rememberFocus(): void {
    const active = this.ownerDocument.activeElement;
    if (active instanceof HTMLElement && !this.contains(active)) {
      this.previousFocus = active;
    }
  }

  /** Applies the live region and the dismiss affordance. */
  render(): void {
    const region = toastLiveRegion(this.severity);
    this.setAttribute("role", region.role);
    this.setAttribute("aria-live", region.ariaLive);
    this.setAttribute("aria-atomic", region.ariaAtomic);
    for (const severity of SEVERITIES) {
      this.classList.toggle(`ga-toast--${severity}`, severity === this.severity);
    }

    const dismissLabel = (this.getAttribute("dismiss-label") ?? "").trim();
    const dismiss = ensureChild<HTMLButtonElement>(this, DISMISS_SELECTOR, "button");
    dismiss.setAttribute("data-ga-dismiss", "");
    dismiss.setAttribute("type", "button");
    dismiss.classList.add(DISMISS_CLASS);
    if (dismissLabel === "") {
      // Without a translated name the button would be unusable for assistive
      // technology, so it is not offered at all.
      dismiss.removeAttribute("aria-label");
      toggleHidden(dismiss, true);
    } else {
      dismiss.setAttribute("aria-label", dismissLabel);
      toggleHidden(dismiss, false);
    }
  }

  /** (Re)schedules dismissal from the current interaction state. */
  schedule(): void {
    this.clearTimer();
    if (!this.isConnected) {
      return;
    }
    const timing = toastTiming(this.severity, this.durationMs(), {
      focused: this.contains(this.ownerDocument.activeElement),
      hovered: this.hovered,
    });
    if (timing.dismissAfterMs === null || timing.paused) {
      return;
    }
    this.timer = setTimeout(() => {
      this.dismiss();
    }, timing.dismissAfterMs);
  }

  /** Removes the toast and restores focus when it belonged to the user. */
  dismiss(): void {
    this.clearTimer();
    const hadFocusInside = this.hadFocusInside || this.contains(this.ownerDocument.activeElement);
    const previous = this.previousFocus;
    const restore = shouldRestoreFocus({
      hadFocusInside,
      previousFocusConnected: previous !== null && previous.isConnected,
    });
    this.remove();
    if (restore && previous !== null) {
      previous.focus();
    }
  }

  /** Duration in milliseconds, defaulting to the primitive's own default. */
  private durationMs(): number {
    const raw = this.getAttribute("duration");
    if (raw === null) {
      return TOAST_DEFAULT_DURATION_MS;
    }
    const parsed = Number.parseInt(raw.trim(), 10);
    return Number.isFinite(parsed) ? parsed : TOAST_DEFAULT_DURATION_MS;
  }

  /** Cancels the pending dismissal, if any. */
  private clearTimer(): void {
    if (this.timer !== null) {
      clearTimeout(this.timer);
      this.timer = null;
    }
  }

  private readonly onPointerEnter = (): void => {
    this.hovered = true;
    this.schedule();
  };

  private readonly onPointerLeave = (): void => {
    this.hovered = false;
    this.schedule();
  };

  private readonly onFocusChange = (): void => {
    if (this.contains(this.ownerDocument.activeElement)) {
      this.hadFocusInside = true;
    }
    this.schedule();
  };

  private readonly onKeyDown = (event: KeyboardEvent): void => {
    if (event.key === "Escape") {
      event.stopPropagation();
      this.dismiss();
    }
  };

  private readonly onClick = (event: Event): void => {
    const target = event.target;
    if (target instanceof Element && target.closest(DISMISS_SELECTOR) !== null) {
      this.dismiss();
    }
  };
}
