/**
 * `ga-realm-navigation` adopts the server-rendered navigation in Light DOM.
 * The button is enhanced only after connection; without JS links stay visible.
 * Owns aria-expanded/data-open only. Disconnect aborts all owned listeners.
 * Escape closes the mobile drawer and restores focus to its trigger.
 */
export class GaRealmNavigationElement extends HTMLElement {
  private events: AbortController | undefined;

  connectedCallback(): void {
    this.events?.abort();
    this.events = new AbortController();
    const button = this.querySelector<HTMLButtonElement>("button[aria-controls]");
    if (button === null) return;
    button.hidden = false;
    const options = { signal: this.events.signal };
    button.addEventListener("click", () => {
      this.setOpen(button, button.getAttribute("aria-expanded") !== "true");
    }, options);
    this.addEventListener("keydown", (event) => {
      if (event.key === "Escape" && this.hasAttribute("data-open")) {
        this.setOpen(button, false);
        button.focus();
      }
    }, options);
    this.addEventListener("click", (event) => {
      if (event.target instanceof Element && event.target.closest("a") !== null) {
        this.setOpen(button, false);
      }
    }, options);
  }

  disconnectedCallback(): void {
    this.events?.abort();
    this.events = undefined;
  }

  private setOpen(button: HTMLButtonElement, open: boolean): void {
    button.setAttribute("aria-expanded", String(open));
    this.toggleAttribute("data-open", open);
  }
}

if (typeof customElements !== "undefined" && customElements.get("ga-realm-navigation") === undefined) {
  customElements.define("ga-realm-navigation", GaRealmNavigationElement);
}
