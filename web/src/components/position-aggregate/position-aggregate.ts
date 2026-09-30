/**
 * `ga-position-aggregate` — public aggregate of one Arena (P18-T06 journey,
 * "Visitante entende uma Arena").
 *
 * Tag: `ga-position-aggregate`.
 *
 * Responsibility: present the aggregate the page fetched through the positions
 * client, using only the translated text its view carries. It fetches nothing,
 * translates nothing and decides nothing about the language of the document.
 *
 * Properties:
 *   view   the `AggregateView` the page computed, or null. Setting it while
 *          connected re-renders.
 *
 * Events: none. The element is presentational and emits no intent.
 *
 * States: published (heading, total, both distributions and the derivation
 * instant), suppressed (heading, suppression note and instant, never a count)
 * and empty (no view). There is no loading or error state: the page owns the
 * request and reveals the element only when it has an answer, so the element
 * never shows a spinner it cannot clear.
 *
 * Keyboard and focus: nothing inside is focusable. `focusHeading()` moves focus
 * to the heading (`tabindex="-1"`), which is how a reveal that replaced what
 * had focus announces itself without leaving the reader at the top of the
 * document.
 *
 * CSS: `ga-position-aggregate` and its `__` elements, styled by the page sheet
 * (`arena.css`) with the tokens of `tokens.css`.
 *
 * External effects: none; there is nothing to cancel in `disconnectedCallback`.
 *
 * Usage:
 *   definePositionAggregate();
 *   const element = document.createElement(POSITION_AGGREGATE_TAG);
 *   element.view = aggregatePresentation(translator, locale, aggregate);
 *   section.replaceChildren(element);
 *   element.focusHeading();
 */
import { setText } from "../primitives/dom.js";
import type { AggregateRow, AggregateView } from "./model.js";

/** The custom element tag, defined once per document. */
export const POSITION_AGGREGATE_TAG = "ga-position-aggregate";

/** Builds the heading and the definition list of one distribution. */
function distributionElement(
  document: Document,
  heading: string,
  rows: readonly AggregateRow[],
  key: string,
): DocumentFragment {
  const fragment = document.createDocumentFragment();
  const title = document.createElement("h3");
  title.className = "ga-position-aggregate__distribution-heading";
  setText(title, heading);

  const list = document.createElement("dl");
  list.className = "ga-position-aggregate__rows";
  list.setAttribute("data-ga-distribution", key);
  for (const row of rows) {
    const label = document.createElement("dt");
    label.className = "ga-position-aggregate__label";
    setText(label, row.label);
    const count = document.createElement("dd");
    count.className = "ga-position-aggregate__count";
    setText(count, row.count);
    list.append(label, count);
  }

  fragment.append(title, list);
  return fragment;
}

/** One paragraph of the aggregate presentation. */
function paragraph(document: Document, className: string, text: string): HTMLParagraphElement {
  const element = document.createElement("p");
  element.className = className;
  setText(element, text);
  return element;
}

export class GaPositionAggregateElement extends HTMLElement {
  #view: AggregateView | null = null;

  get view(): AggregateView | null {
    return this.#view;
  }

  set view(value: AggregateView | null) {
    this.#view = value;
    if (this.isConnected) {
      this.render();
    }
  }

  connectedCallback(): void {
    this.classList.add("ga-position-aggregate");
    this.render();
  }

  /** Moves focus to the heading, after a reveal replaced what had focus. */
  focusHeading(): void {
    const heading = this.querySelector("h2");
    if (heading === null) {
      return;
    }
    heading.setAttribute("tabindex", "-1");
    heading.focus();
  }

  private render(): void {
    const view = this.#view;
    if (view === null) {
      this.replaceChildren();
      return;
    }

    const document = this.ownerDocument;
    const heading = document.createElement("h2");
    heading.className = "ga-position-aggregate__heading";
    setText(heading, view.heading);

    const nodes: Node[] = [heading];
    if (view.suppressedNote !== null) {
      nodes.push(paragraph(document, "ga-position-aggregate__note", view.suppressedNote));
    }
    if (view.total !== null) {
      nodes.push(paragraph(document, "ga-position-aggregate__total", view.total));
    }
    if (view.currentHeading !== null) {
      nodes.push(distributionElement(document, view.currentHeading, view.current, "current"));
    }
    if (view.initialHeading !== null) {
      nodes.push(distributionElement(document, view.initialHeading, view.initial, "initial"));
    }
    nodes.push(paragraph(document, "ga-position-aggregate__checked", view.checked));
    this.replaceChildren(...nodes);
  }
}

/** Defines the element once; a second call is a no-op and a reload stays safe. */
export function definePositionAggregate(registry: CustomElementRegistry = customElements): void {
  if (registry.get(POSITION_AGGREGATE_TAG) === undefined) {
    registry.define(POSITION_AGGREGATE_TAG, GaPositionAggregateElement);
  }
}
