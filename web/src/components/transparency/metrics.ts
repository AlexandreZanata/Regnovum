/**
 * `ga-transparency-metrics` — staged economic exhibit (P38-T08).
 *
 * Attributes (all already translated strings, never amounts to
 * compute):
 *
 *   data-title       exhibit title
 *   data-window      sealed UTC window line
 *   data-supply      supply line
 *   data-treasury    treasury line
 *   data-circulation circulation line
 *   data-r4          R4 reference line
 *   data-crumbs      crumbs granted line
 *   data-irr         reflux index line
 *
 * The element is staged like its API: defined here, mounted only
 * after the activation gate. Dynamic values reach the document
 * through `textContent` only.
 */
import { metricsPresentation } from "./model.js";

function readText(element: HTMLElement, name: string): string {
  const value = element.getAttribute(name);
  if (value === null || value.trim() === "") {
    return "";
  }
  return value;
}

export class GaTransparencyMetricsElement extends HTMLElement {
  static observedAttributes: readonly string[] = [
    "data-title",
    "data-window",
    "data-supply",
    "data-treasury",
    "data-circulation",
    "data-r4",
    "data-crumbs",
    "data-irr",
  ];

  connectedCallback(): void {
    this.classList.add("ga-transparency-metrics");
    this.render();
  }

  attributeChangedCallback(): void {
    if (this.isConnected) {
      this.render();
    }
  }

  private render(): void {
    const exhibit = metricsPresentation({
      title: readText(this, "data-title"),
      window: readText(this, "data-window"),
      supply: readText(this, "data-supply"),
      treasury: readText(this, "data-treasury"),
      circulation: readText(this, "data-circulation"),
      r4: readText(this, "data-r4"),
      crumbs: readText(this, "data-crumbs"),
      irr: readText(this, "data-irr"),
    });
    while (this.firstChild !== null) {
      this.removeChild(this.firstChild);
    }
    this.setAttribute("role", exhibit.role);
    for (const line of exhibit.lines) {
      const paragraph = document.createElement("p");
      paragraph.textContent = line.text;
      this.appendChild(paragraph);
    }
  }
}

if (typeof customElements !== "undefined") {
  customElements.define("ga-transparency-metrics", GaTransparencyMetricsElement);
}
