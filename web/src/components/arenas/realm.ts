/**
 * `ga-arena-realm` — staged realm exhibit (P39-T03).
 *
 * Attributes (all already translated strings, never values to
 * compute):
 *
 *   data-kingdom  the single Kingdom line
 *   data-arena    the debate-instance line
 *   data-note     the no-winner line
 *
 * The element is staged like its API: defined here, mounted only
 * after the activation gate. Dynamic values reach the document
 * through `textContent` only.
 */
import { realmPresentation } from "./model.js";

function readText(element: HTMLElement, name: string): string {
  const value = element.getAttribute(name);
  if (value === null || value.trim() === "") {
    return "";
  }
  return value;
}

export class GaArenaRealmElement extends HTMLElement {
  static observedAttributes: readonly string[] = ["data-kingdom", "data-arena", "data-note"];

  connectedCallback(): void {
    this.classList.add("ga-arena-realm");
    this.render();
  }

  attributeChangedCallback(): void {
    if (this.isConnected) {
      this.render();
    }
  }

  private render(): void {
    const exhibit = realmPresentation({
      kingdom: readText(this, "data-kingdom"),
      arena: readText(this, "data-arena"),
      note: readText(this, "data-note"),
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
  customElements.define("ga-arena-realm", GaArenaRealmElement);
}
