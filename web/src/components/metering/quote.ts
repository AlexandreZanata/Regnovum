/**
 * `ga-metering-quote` — staged metering exhibit (P36-T08).
 *
 * Attributes (all already translated strings, never amounts to
 * compute):
 *
 *   data-state   `preview` (default), `confirmed` or `failed`
 *   data-title   exhibit title
 *   data-total   priced total line
 *   data-posted  posting or validity line
 *   data-action  optional action line (confirm button label)
 *
 * The element is staged like its API: defined here, mounted only
 * after the activation gate. Dynamic values reach the document
 * through `textContent` only.
 */
import { exhibitPresentation } from "./model.js";
import type { MeteringExhibitState } from "./model.js";

const STATES: readonly MeteringExhibitState[] = ["preview", "confirmed", "failed"];

function readState(value: string | null): MeteringExhibitState {
  if (value !== null && (STATES as readonly string[]).includes(value)) {
    return value as MeteringExhibitState;
  }
  return "preview";
}

function readText(element: HTMLElement, name: string): string | null {
  const value = element.getAttribute(name);
  if (value === null || value.trim() === "") {
    return null;
  }
  return value;
}

export class GaMeteringQuoteElement extends HTMLElement {
  static observedAttributes: readonly string[] = [
    "data-state",
    "data-title",
    "data-total",
    "data-posted",
    "data-action",
  ];

  connectedCallback(): void {
    this.classList.add("ga-metering-quote");
    this.render();
  }

  attributeChangedCallback(): void {
    if (this.isConnected) {
      this.render();
    }
  }

  private render(): void {
    const exhibit = exhibitPresentation(readState(this.getAttribute("data-state")), {
      title: readText(this, "data-title") ?? "",
      total: readText(this, "data-total") ?? "",
      posted: readText(this, "data-posted") ?? "",
      action: readText(this, "data-action"),
    });
    while (this.firstChild !== null) {
      this.removeChild(this.firstChild);
    }
    this.setAttribute("role", exhibit.role);
    for (const line of exhibit.lines) {
      const paragraph = document.createElement("p");
      paragraph.textContent = line.text;
      if (line.alert) {
        paragraph.setAttribute("role", "alert");
      }
      this.appendChild(paragraph);
    }
  }
}

if (typeof customElements !== "undefined") {
  customElements.define("ga-metering-quote", GaMeteringQuoteElement);
}
