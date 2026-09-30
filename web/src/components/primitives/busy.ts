/**
 * `ga-busy` — busy state of a region (P18-T04).
 *
 * Tag: `ga-busy`.
 *
 * Responsibility: present the busy state of a submission without hiding the
 * content it belongs to; the page switches it with the `busy` attribute.
 *
 * Attributes:
 *
 *   busy   presence marks the region `aria-busy` and shows the indicator
 *   label  optional translated status text, announced through a polite region
 *
 * Events: none. The primitive presents a state the page decided.
 *
 * States: idle (nothing shown) and busy (decorative indicator plus the
 * translated status, announced politely). Content stays visible and readable:
 * a busy region is not a disabled one, and the primitive never invents a
 * message of its own.
 *
 * Keyboard and focus: nothing here is focusable; the submitter and its focus
 * belong to the page.
 *
 * CSS: `ga-busy` and its `__indicator` and `__status` elements, styled by
 * primitives.css with the tokens of tokens.css. The indicator is decorative
 * (`aria-hidden`) and animated only in CSS, with the animation removed under
 * `prefers-reduced-motion: reduce`.
 *
 * External effects: none; the animation lives in CSS, so there is nothing to
 * cancel in `disconnectedCallback`.
 *
 * Usage:
 *   <ga-busy label="Enviando…">
 *     <button type="submit">Entrar</button>
 *   </ga-busy>
 */
import { ensureChild, replaceAttributes, setText, toggleHidden } from "./dom.js";
import { busyPresentation } from "./model.js";
import type { BusyPresentation } from "./model.js";

const OBSERVED_ATTRIBUTES: readonly string[] = ["busy", "label"];
const INDICATOR_SELECTOR = "[data-ga-indicator]";
const STATUS_SELECTOR = "[data-ga-status]";
const INDICATOR_CLASS = "ga-busy__indicator";
const STATUS_CLASS = "ga-busy__status";

export class GaBusyElement extends HTMLElement {
  static observedAttributes: readonly string[] = OBSERVED_ATTRIBUTES;

  connectedCallback(): void {
    this.classList.add("ga-busy");
    this.render();
  }

  attributeChangedCallback(): void {
    if (this.isConnected) {
      this.render();
    }
  }

  /** Applies the presentation computed by the DOM-free model. */
  render(): void {
    const presentation: BusyPresentation = busyPresentation({
      busy: this.hasAttribute("busy"),
      label: this.getAttribute("label"),
    });

    replaceAttributes(this, ["aria-busy"], presentation.hostAttributes);

    const indicator = ensureChild<HTMLElement>(this, INDICATOR_SELECTOR, "span", "start");
    indicator.setAttribute("data-ga-indicator", "");
    indicator.classList.add(INDICATOR_CLASS);
    replaceAttributes(indicator, ["aria-hidden"], presentation.indicatorAttributes);
    toggleHidden(indicator, !presentation.visible);

    const status = ensureChild<HTMLElement>(this, STATUS_SELECTOR, "span");
    status.setAttribute("data-ga-status", "");
    status.classList.add(STATUS_CLASS);
    replaceAttributes(status, ["role", "aria-live", "aria-atomic"], presentation.statusAttributes);
    setText(status, presentation.statusText);
    toggleHidden(status, presentation.statusText === "");
  }
}
