/**
 * The account journey, driven the way a person drives it (P18-T07).
 *
 * Every helper here fills and submits a real form of the server-rendered
 * journey and then asserts a structural consequence of the answer — the notice
 * the submission produced, the session cookie the browser is holding, the page
 * the redirect landed on. Nothing asserts translated prose: the documents are
 * localized, the catalogues own the wording, and a journey that broke every
 * time a message was reworded would be a journey nobody would keep green.
 *
 * The submission guards of `web/src/pages/submission.ts` are deliberately not
 * needed: the forms have real actions, so a click submits them with or without
 * a script, and the browser-side guard only decides the busy state of a
 * submission that is already on its way.
 *
 * Every page the journey reaches is held to the language the context asked for
 * (`expectInterfaceLanguage`), so the same journey is a different assertion in
 * each interface locale and not the same run twice.
 */
import { expect } from "@playwright/test";
import { expectInterfaceLanguage } from "./locales.js";

/** The session cookie the security boundary issues (see internal/platform/security). */
export const SESSION_COOKIE = "arena_session";

/**
 * The form of the account pages and the button that submits it. Scoped to the
 * form itself, because a page may carry more than one form and the journey
 * always means the one it is filling.
 */
const FORM = "form.ga-auth__form";
const SUBMIT = `${FORM} button[type="submit"]`;

/**
 * noticeAction is one of the ways forward the notice page offers, inside the
 * page's own main region.
 *
 * Scoped to `main` and not merely to the page: the header of every account page
 * links to the same four addresses (`/login`, `/register`, `/verify`,
 * `/reset`), so an unscoped `a[href="/verify"]` matches the header link as
 * well as the notice's own action and the runner refuses the ambiguity in
 * strict mode. What the journey asserts is the notice the submission produced,
 * and that notice is inside `main`.
 */
function noticeAction(page, href) {
  return page.locator(`main a[href="${href}"]`);
}

/** fillForm fills the fields of one account form and submits that same form. */
async function fillForm(page, fields) {
  const entries = Object.entries(fields);
  // The form the journey means is the one holding the fields it fills: pages
  // with an account-key entry carry two forms, and a page-wide submitter would
  // match both. Scoping by the first field keeps the documented contract —
  // one form per submission — without naming any translated button.
  const form = page.locator(FORM, { has: page.locator(`input[name="${entries[0][0]}"]`) });
  for (const [name, value] of entries) {
    await form.locator(`input[name="${name}"]`).fill(value);
  }
  await form.locator('button[type="submit"]').click();
}

/**
 * register creates an account through the form and waits for the notice the
 * server answers with. The notice is the uniform answer to a registration —
 * the addresses it carries are the only way forward, and the confirmation form
 * is behind one of them.
 */
export async function register(page, { email, password }) {
  await page.goto("/register");
  await expectInterfaceLanguage(page, "the registration form");
  await fillForm(page, { email, password });
  await expect(noticeAction(page, "/verify")).toBeVisible();
  await expectInterfaceLanguage(page, "the registration notice");
}

/**
 * confirm consumes the confirmation code through the form. The code is read
 * from the sink directory by the caller: the message was delivered by another
 * process, and reading it from outside is the point of the directory sink.
 */
export async function confirm(page, code) {
  await page.goto("/verify");
  await expectInterfaceLanguage(page, "the confirmation form");
  await fillForm(page, { token: code });
  await expect(noticeAction(page, "/login")).toBeVisible();
  await expectInterfaceLanguage(page, "the confirmation notice");
}

/**
 * signIn signs in and returns once the server has answered with its redirect.
 * The evidence of the session is the cookie the security boundary issued, not
 * the address of the landing page: the authenticated home belongs to another
 * microtask of the phase, and a journey that asserted it would fail for a
 * reason that has nothing to do with signing in.
 */
export async function signIn(page, { email, password }) {
  await page.goto("/login");
  await expectInterfaceLanguage(page, "the sign-in form");
  await fillForm(page, { email, password });
  await page.waitForURL("/");
  await expect.poll(() => sessionCookie(page)).not.toBeNull();
}

/** signOut ends the session through the confirmation form of `/logout`. */
export async function signOut(page) {
  await page.goto("/logout");
  await expectInterfaceLanguage(page, "the sign-out form");
  await Promise.all([page.waitForURL(/\/login$/), page.locator(SUBMIT).click()]);
  await expect.poll(() => sessionCookie(page)).toBeNull();
  // The page the sign-out landed on is the one a signed-out person starts from
  // again, so it is asserted too: it is a page of the journey, not a redirect.
  await expectInterfaceLanguage(page, "the page the sign-out landed on");
}

/** sessionCookie returns the session cookie the context is holding, if any. */
export async function sessionCookie(page) {
  const cookies = await page.context().cookies();
  return cookies.find((cookie) => cookie.name === SESSION_COOKIE) ?? null;
}
