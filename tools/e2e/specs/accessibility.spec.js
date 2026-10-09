/**
 * The accessibility smoke of the account and Arena families in a real
 * browser (P58-T02), in every interface locale the product ships.
 *
 * What this smoke is for: the unit tests prove the DOM-free decisions — the
 * wiring the primitives compute, the translated views the pages render — but
 * only a browser can prove that a person driving the page with a keyboard
 * reaches the content, that a refused submission moves focus to the errors,
 * that every control answers to a name, that a 360px viewport (and the 640px
 * width a 200% zoom leaves on a 1280px screen) never scrolls sideways, and
 * that reduced motion really stops the busy indicator.
 *
 * What it is not: a full audit of every family. P58-T02 closes the
 * keyboard/focus/labels/overflow/motion posture on the account (P50) and
 * Arena (P52) families and records the segmented smoke; the remaining
 * families reuse the same primitives and shell, and their journeys stay
 * green in the specs that own them.
 *
 * Every test drives the page the way a person does — no selector reaches
 * into implementation markup, and no test invents a route: the refused login
 * posts credentials the server never issued, and the Arena page is the one
 * the harness published for the locale.
 */
import { expect, test } from "@playwright/test";
import { arenaSlugFor, participationEnvironment } from "../support/environment.js";
import { JOURNEY_LOCALES } from "../support/locales.js";

/** Viewport widths the overflow assertions drive: a small phone and the width a 200% zoom leaves. */
const NARROW_WIDTHS = [360, 640];

/** True when the document never scrolls sideways at its current viewport. */
async function noHorizontalOverflow(page) {
  return page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth);
}

for (const locale of JOURNEY_LOCALES) {
  test(`keyboard reaches the content through the skip link (${locale})`, async ({ browser }) => {
    const context = await browser.newContext({ locale });
    try {
      const page = await context.newPage();
      await page.goto("/login");
      // The first stop from the address bar is the skip link, and it is
      // visible when focused: a keyboard user never has to guess where they are.
      await page.keyboard.press("Tab");
      const skip = page.locator(".ga-shell__skip").first();
      await expect(skip).toBeFocused();
      await expect(skip).toBeVisible();
      await page.keyboard.press("Enter");
      const focused = await page.evaluate(
        () => `${document.activeElement?.tagName ?? ""}#${document.activeElement?.id ?? ""}`,
      );
      expect(focused).toBe("MAIN#main");
    } finally {
      await context.close();
    }
  });

  test(`a refused login moves focus to the error summary (${locale})`, async ({ browser }) => {
    const context = await browser.newContext({ locale });
    try {
      const page = await context.newPage();
      await page.goto("/login");
      await page.locator('input[name="email"]').fill(`unknown-${locale.toLowerCase()}@example.test`);
      await page.locator('input[name="password"]').fill("wrong password");
      // The sign-in page carries two forms since the account-key entry
      // (key first, email and password second): submit the one holding the
      // refused credentials, not the first submitter of the page.
      await page.locator('form.ga-auth__form', { has: page.locator('input[name="password"]') }).locator('button[type="submit"]').click();
      // The server refuses the submission with a full page, and the summary
      // the page arrived with takes focus: the errors are where the person is.
      const summary = page.locator("ga-error-summary").first();
      await expect(summary).toBeVisible();
      await expect(summary).toBeFocused();
    } finally {
      await context.close();
    }
  });

  test(`every control on the registration form answers to a name (${locale})`, async ({ browser }) => {
    const context = await browser.newContext({ locale });
    try {
      const page = await context.newPage();
      await page.goto("/register");
      const controls = page.locator("input:not([type='hidden']), select, textarea, button");
      const count = await controls.count();
      expect(count).toBeGreaterThan(0);
      for (let index = 0; index < count; index += 1) {
        const name = await controls.nth(index).evaluate((element) => {
          if (element instanceof HTMLInputElement && element.labels !== null) {
            return [...element.labels].map((label) => label.textContent ?? "").join(" ");
          }
          return element.getAttribute("aria-label") ?? element.textContent ?? "";
        });
        expect(name.trim()).not.toBe("");
      }
    } finally {
      await context.close();
    }
  });

  test(`reduced motion stops the busy indicator (${locale})`, async ({ browser }) => {
    const context = await browser.newContext({ locale, reducedMotion: "reduce" });
    try {
      const page = await context.newPage();
      await page.goto("/login");
      const animation = await page.locator(".ga-busy__indicator").first().evaluate(
        (element) => getComputedStyle(element).animationName,
      );
      expect(animation === "none" || animation === "").toBe(true);
    } finally {
      await context.close();
    }
  });

  for (const width of NARROW_WIDTHS) {
    test(`no sideways scrolling at ${width}px on the account and Arena surfaces (${locale})`, async ({
      browser,
    }) => {
      const environment = participationEnvironment();
      const slug = arenaSlugFor(locale);
      const context = await browser.newContext({
        locale,
        viewport: { width, height: 800 },
      });
      try {
        const page = await context.newPage();
        await page.goto("/login");
        expect(await noHorizontalOverflow(page)).toBe(true);
        await page.goto("/register");
        expect(await noHorizontalOverflow(page)).toBe(true);
        // A visitor, not a signed-in participant: the aggregate reveals in
        // place when the server-rendered reveal link is pressed.
        await page.goto(`/arenas/${slug}`);
        await page.locator('a[href*="reveal=1"]').first().click();
        await page.locator("ga-position-aggregate").first().waitFor();
        expect(await noHorizontalOverflow(page)).toBe(true);
      } finally {
        await context.close();
      }
    });
  }

  test(`the Arena aggregate reveal moves focus to its heading (${locale})`, async ({ browser }) => {
    const environment = participationEnvironment();
    const slug = arenaSlugFor(locale);
    const context = await browser.newContext({ locale });
    try {
      const page = await context.newPage();
      // A visitor, not a signed-in participant: the signed-in page keeps the
      // reveal a navigation, so only the visitor journey exercises the
      // in-place reveal and its focus move.
      await page.goto(`/arenas/${slug}`);
      await page.locator('a[href*="reveal=1"]').first().click();
      // The aggregate replaces what had focus when it arrives, so the page
      // moves focus to its heading instead of leaving the reader behind.
      const heading = page.locator("ga-position-aggregate h2").first();
      await expect(heading).toBeFocused();
    } finally {
      await context.close();
    }
  });
}
