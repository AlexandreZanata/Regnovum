/**
 * `ga-season-champions` — staged privacy-safe champions exhibit (P47-T08).
 *
 * Attributes (all already translated strings or pseudonyms, never exact
 * wealth):
 *
 *   data-title       exhibit title
 *   data-richest     richest-at-cutoff label
 *   data-last-king   last-King label
 *   data-leaders     comma-separated co-leader pseudonyms
 *
 * The element is staged like its API: defined here, mounted only after
 * the activation gate. Dynamic values reach the document through
 * `textContent` only.
 */
import { championsPresentation } from "./model.js";

function readText(element: HTMLElement, name: string): string {
  const value = element.getAttribute(name);
  if (value === null || value.trim() === "") {
    return "";
  }
  return value;
}

function readLeaders(element: HTMLElement): readonly string[] {
  const raw = element.getAttribute("data-leaders");
  if (raw === null || raw.trim() === "") {
    return [];
  }
  return raw
    .split(",")
    .map((part) => part.trim())
    .filter((part) => part !== "");
}

export class GaSeasonChampionsElement extends HTMLElement {
  static observedAttributes: readonly string[] = [
    "data-title",
    "data-richest",
    "data-last-king",
    "data-leaders",
  ];

  connectedCallback(): void {
    this.classList.add("ga-season-champions");
    this.render();
  }

  attributeChangedCallback(): void {
    if (this.isConnected) {
      this.render();
    }
  }

  private render(): void {
    const exhibit = championsPresentation({
      title: readText(this, "data-title"),
      richest: readText(this, "data-richest"),
      lastKing: readText(this, "data-last-king"),
      leaders: readLeaders(this),
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
  customElements.define("ga-season-champions", GaSeasonChampionsElement);
}
