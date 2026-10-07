/**
 * Tests of the application shell decisions (P50-T01).
 *
 * The shell is a server-rendered document that works without any script, so
 * what is proven here is what the module adds on top: the stable address of a
 * deep link, the current-page mark derived from it, the session visibility of
 * the navigation (which filters emphasis, never authorization), the expired
 * session signal and the cache policy that keeps account chrome out of shared
 * stores. The element wiring is not executed — it cannot be, without a
 * browser — and that is the reason every decision lives in a DOM-free
 * function.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  SHELL_MAIN_ID,
  SHELL_SKIP_HREF,
  SIGN_IN_HREF,
  isCurrentShellLink,
  normalizeShellPath,
  sessionSignalFromStatus,
  shellCachePolicy,
  signInTarget,
  visibleShellLinks,
} from "../../src/pages/shell.js";
import type { ShellLink } from "../../src/pages/shell.js";

const NAV: readonly ShellLink[] = [
  { href: "/login", guestOnly: true },
  { href: "/logout", requiresAuth: true },
  { href: "/register", guestOnly: true },
  { href: "/verify" },
  { href: "/reset" },
];

test("the skip link points at the main landmark", () => {
  assert.equal(SHELL_SKIP_HREF, `#${SHELL_MAIN_ID}`);
});

test("an empty address is the root, so entries and deep links agree", () => {
  assert.equal(normalizeShellPath(""), "/");
  assert.equal(normalizeShellPath("/"), "/");
});

test("the stable address drops the query, the fragment and the trailing slash", () => {
  assert.equal(normalizeShellPath("/login?next=/arenas/x"), "/login");
  assert.equal(normalizeShellPath("/login#main"), "/login");
  assert.equal(normalizeShellPath("/login/"), "/login");
  assert.equal(normalizeShellPath("login"), "/login");
});

test("the current mark follows the normalized address only", () => {
  assert.equal(isCurrentShellLink("/login", "/login"), true);
  assert.equal(isCurrentShellLink("/login/", "/login?next=/"), true);
  assert.equal(isCurrentShellLink("/reset", "/reset/confirm"), false);
  assert.equal(isCurrentShellLink("", "/login"), false);
  assert.equal(isCurrentShellLink("/login", "/register"), false);
});

test("a visitor is offered the entries, never the way out", () => {
  const visible = visibleShellLinks(NAV, { signedIn: false }).map((link) => link.href);

  assert.deepEqual(visible, ["/login", "/register", "/verify", "/reset"]);
});

test("a signed-in browser is offered the way out, never the entries", () => {
  const visible = visibleShellLinks(NAV, { signedIn: true }).map((link) => link.href);

  assert.deepEqual(visible, ["/logout", "/verify", "/reset"]);
});

test("filtering never rewrites the links it keeps", () => {
  const visible = visibleShellLinks(NAV, { signedIn: true });

  for (const link of visible) {
    const original = NAV.find((candidate) => candidate.href === link.href);
    assert.equal(link, original, "the kept link must be the declared one, not a copy");
  }
});

test("only a 401 is an expired session", () => {
  assert.equal(sessionSignalFromStatus(401), "expired");
  for (const status of [null, 200, 302, 403, 404, 409, 422, 429, 500]) {
    assert.equal(sessionSignalFromStatus(status), null, `status ${String(status)} must not expire the session`);
  }
});

test("an expired session returns to the sign-in entry", () => {
  assert.equal(signInTarget(), SIGN_IN_HREF);
  assert.equal(signInTarget(), "/login");
});

test("account chrome is never stored, public reads keep the cache", () => {
  for (const path of ["/login", "/login/", "/register", "/verify", "/reset", "/reset/confirm", "/logout"]) {
    assert.equal(shellCachePolicy(path), "no-store", `${path} must never be stored`);
  }
  for (const path of ["/", "/arenas/slug", "/api/v1/health"]) {
    assert.equal(shellCachePolicy(path), "default", `${path} must keep the HTTP cache`);
  }
});

test("a sibling of an account address is not an account address", () => {
  assert.equal(shellCachePolicy("/logins"), "default");
  assert.equal(shellCachePolicy("/verification"), "default");
});
