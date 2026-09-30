/**
 * `ga-commerce-receipt` — staged trade exhibit (P37-T07).
 *
 * Attributes (all already translated strings, never amounts to
 * compute):
 *
 *   data-state   `pending` (default), `settled` or `failed`
 *   data-title   exhibit title
 *   data-gross   gross line
 *   data-tithe   tithe line
 *   data-net     net line
 *   data-posted  posting or settlement line
 *   data-action  optional action line (appeal label)
 *
 * The element is staged like its API: defined here, mounted only
 * after the activation gate. Dynamic values reach the document
 * through `textContent` only.
 */
import { tradePresentation } from "./model.js";
import type { TradeExhibitState } from "./model.js";

const STATES: readonly TradeExhibitState[] = ["pending", "settled", "failed"];

function readState(value: string | null): TradeExhibitState {
  if (value !== null && (STATES as readonly string[]).includes(value)) {
    return value as TradeExhibitState;
  }
  return "pending";
}

function readText(element: HTMLElement, name: string): string | null {
  const value = element.getAttribute(name);
  if (value === null || value.trim() === "") {
    return null;
  }
  return value;
}

export class GaCommerceReceiptElement extends HTMLElement {
  static observedAttributes: readonly string[] = [
    "data-state",
    "data-title",
    "data-gross",
    "data-tithe",
    "data-net",
    "data-posted",
    "data-action",
  ];

  connectedCallback(): void {
    this.classList.add("ga-commerce-receipt");
    this.render();
  }

  attributeChangedCallback(): void {
    if (this.isConnected) {
      this.render();
    }
  }

  private render(): void {
    const exhibit = tradePresentation(readState(this.getAttribute("data-state")), {
      title: readText(this, "data-title") ?? "",
      gross: readText(this, "data-gross") ?? "",
      tithe: readText(this, "data-tithe") ?? "",
      net: readText(this, "data-net") ?? "",
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
  customElements.define("ga-commerce-receipt", GaCommerceReceiptElement);
}
