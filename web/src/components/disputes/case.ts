/**
 * `ga-dispute-case` — staged private case exhibit (P39-T08).
 *
 * Attributes (all already translated strings, never values to
 * compute):
 *
 *   data-state   `proposed` (default), `open`, `decided` or `appealed`
 *   data-title   exhibit title
 *   data-proposal proposal line
 *   data-parties  parties line
 *   data-decided  decision line
 *   data-action  optional action line (appeal label)
 *
 * The element is staged like its API: defined here, mounted only
 * after the activation gate. Dynamic values reach the document
 * through `textContent` only.
 */
import { disputePresentation } from "./model.js";
import type { DisputeExhibitState } from "./model.js";

const STATES: readonly DisputeExhibitState[] = [
  "proposed",
  "open",
  "decided",
  "appealed",
];

function readState(value: string | null): DisputeExhibitState {
  if (value !== null && (STATES as readonly string[]).includes(value)) {
    return value as DisputeExhibitState;
  }
  return "proposed";
}

function readText(element: HTMLElement, name: string): string | null {
  const value = element.getAttribute(name);
  if (value === null || value.trim() === "") {
    return null;
  }
  return value;
}

export class GaDisputeCaseElement extends HTMLElement {
  static observedAttributes: readonly string[] = [
    "data-state",
    "data-title",
    "data-proposal",
    "data-parties",
    "data-decided",
    "data-action",
  ];

  connectedCallback(): void {
    this.classList.add("ga-dispute-case");
    this.render();
  }

  attributeChangedCallback(): void {
    if (this.isConnected) {
      this.render();
    }
  }

  private render(): void {
    const exhibit = disputePresentation(readState(this.getAttribute("data-state")), {
      title: readText(this, "data-title") ?? "",
      proposal: readText(this, "data-proposal") ?? "",
      parties: readText(this, "data-parties") ?? "",
      decided: readText(this, "data-decided") ?? "",
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
  customElements.define("ga-dispute-case", GaDisputeCaseElement);
}
