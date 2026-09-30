/**
 * `ga-field` — accessible form field (P18-T04).
 *
 * Tag: `ga-field`.
 *
 * Responsibility: complete the wiring of one field the server already
 * rendered — its label, control, hint and error — so assistive technology can
 * name the control and read its messages.
 *
 * Attributes (every text is already translated by the page, never here):
 *
 *   name      identifier base; the control becomes `<name>-control` (required)
 *   label     visible label text (required)
 *   hint      optional help text, linked through `aria-describedby`
 *   error     optional error text; sets `aria-invalid` and announces as alert
 *   required  presence sets `aria-required` on the control
 *
 * Events: none. The element completes markup; the form owns the submission.
 *
 * States: quiet (valid and optional), hinted (described), invalid (described
 * and announced) and required (`aria-required`). There is no loading state:
 * the page owns submissions and `ga-busy` presents them.
 *
 * Keyboard and focus: nothing beyond the control is focusable, and the control
 * keeps its native tab order; the element never moves focus on its own.
 *
 * CSS: `ga-field` and its `__label`, `__control`, `__hint` and `__error`
 * elements, styled by primitives.css with the tokens of tokens.css.
 *
 * External effects: none; there is nothing to cancel in `disconnectedCallback`.
 *
 * Light DOM contract: the element contains a form control — an element with
 * `data-ga-control`, or the first `input`/`select`/`textarea` belonging to this
 * field — and may already contain the label and the message paragraphs the
 * server rendered. The element adopts those nodes and completes the wiring, so
 * the form is usable before the script runs and stays identical after it.
 *
 * Usage:
 *   <ga-field name="email" label="E-mail" required>
 *     <label for="email-control">E-mail</label>
 *     <input id="email-control" name="email" />
 *   </ga-field>
 */
import { ensureChild, findControl, replaceAttributes, setText, toggleHidden } from "./dom.js";
import { fieldWiring } from "./model.js";
import type { FieldWiring, PrimitiveMessage } from "./model.js";

/** Attributes this element owns on the control; a render resets them all. */
const MANAGED_CONTROL_ATTRIBUTES: readonly string[] = ["id", "aria-describedby", "aria-required", "aria-invalid"];
const OBSERVED_ATTRIBUTES: readonly string[] = ["name", "label", "hint", "error", "required"];

/** Class hooks that keep the styling out of the behaviour. */
const LABEL_CLASS = "ga-field__label";
const CONTROL_CLASS = "ga-field__control";
const MESSAGE_ATTRIBUTE = "data-ga-message";

export class GaFieldElement extends HTMLElement {
  static observedAttributes: readonly string[] = OBSERVED_ATTRIBUTES;

  connectedCallback(): void {
    this.render();
  }

  attributeChangedCallback(): void {
    if (this.isConnected) {
      this.render();
    }
  }

  /** Re-reads the attributes and applies the wiring, or fails closed. */
  render(): void {
    let wiring: FieldWiring;
    try {
      wiring = fieldWiring({
        name: this.getAttribute("name") ?? "",
        label: this.getAttribute("label") ?? "",
        hint: this.getAttribute("hint"),
        error: this.getAttribute("error"),
        required: this.hasAttribute("required"),
      });
    } catch (cause) {
      // A field without a usable label would be invisible to assistive
      // technology, so nothing is applied and the misuse is reported.
      this.report(cause);
      return;
    }

    const label = ensureChild<HTMLLabelElement>(this, ":scope > label", "label", "start");
    label.classList.add(LABEL_CLASS);
    setText(label, wiring.labelText);
    replaceAttributes(label, ["for"], wiring.labelAttributes);

    const control = findControl(this);
    if (control === null) {
      this.report(new TypeError("ga-field: no form control found inside the field"));
      return;
    }
    control.classList.add(CONTROL_CLASS);

    const managed: string[] = [];
    const wanted: Record<string, string> = {};
    for (const name of MANAGED_CONTROL_ATTRIBUTES) {
      managed.push(name);
      const value = wiring.controlAttributes[name];
      if (value !== undefined) {
        wanted[name] = value;
      }
    }
    replaceAttributes(control, managed, wanted);

    const rendered = new Set<string>();
    for (const message of wiring.messages) {
      rendered.add(message.id);
      this.renderMessage(message);
    }
    this.hideStaleMessages(rendered);
  }

  /** Applies one hint or error paragraph. */
  private renderMessage(message: PrimitiveMessage): void {
    const element = ensureChild<HTMLParagraphElement>(this, `[id="${message.id}"]`, "p");
    element.setAttribute(MESSAGE_ATTRIBUTE, message.kind);
    element.classList.add(message.kind === "hint" ? "ga-field__hint" : "ga-field__error");
    if (message.alert) {
      element.setAttribute("role", "alert");
    } else {
      element.removeAttribute("role");
    }
    setText(element, message.text);
    toggleHidden(element, false);
  }

  /** Hides messages the current state no longer declares. */
  private hideStaleMessages(rendered: ReadonlySet<string>): void {
    for (const element of this.querySelectorAll(`[${MESSAGE_ATTRIBUTE}]`)) {
      if (!rendered.has(element.id)) {
        setText(element, "");
        toggleHidden(element, true);
      }
    }
  }

  /** Reports a misuse without throwing inside a lifecycle callback. */
  private report(cause: unknown): void {
    console.error(cause);
  }
}
