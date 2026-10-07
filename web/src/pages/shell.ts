/**
 * Decisions and enhancer of the application shell (P50-T01).
 *
 * The shell — skip link, header, navigation, footer and page measure — is a
 * server-rendered document first: every link is a real anchor with a stable
 * URL, the navigation works with JavaScript switched off, and every page is
 * answered private and never stored (the adapter renders through
 * `websurface.WritePrivate`, which the adapter suite proves). What a script
 * can add, and all this module decides, are the three things a document
 * cannot do by itself:
 *
 *   - marking the navigation link of the address the visitor is on
 *     (`aria-current="page"`), including after a back-forward-cache restore,
 *     so a reload, a deep link and the back button all agree on where the
 *     person is;
 *   - choosing which links the chrome emphasizes from the session the browser
 *     holds. Hiding a link is never the authorization: the server still
 *     enforces every transition, and a person who follows a hidden address by
 *     hand meets the same refusal as before;
 *   - naming where an expired session goes. The HTTP core reports the 401
 *     through `onUnauthorized`; this module only decides the address, so the
 *     core and the chrome cannot disagree about it.
 *
 * Keeping the decisions here means the Node runner verifies them without a
 * browser: the wiring below only applies what is computed above, writes
 * attributes (never markup or text) and never touches the network — components
 * and pages reach the server through `core/http.ts` only.
 */

/** Identifier of the main landmark the skip link points at. */
export const SHELL_MAIN_ID = "main";

/** Fragment of the skip link; the stable address of the main content. */
export const SHELL_SKIP_HREF = "#main";

/** Address of the sign-in page, the single entry of an expired session. */
export const SIGN_IN_HREF = "/login";

/** Marker of a document whose shell is already installed. */
const SHELL_GUARD_ATTRIBUTE = "data-ga-shell";

/** Attribute marking the navigation link of the current address. */
const CURRENT_ATTRIBUTE = "aria-current";

/** Value marking the navigation link of the current address. */
const CURRENT_VALUE = "page";

/** The session as the shell sees it: presence of a signed-in browser. */
export interface ShellSession {
  readonly signedIn: boolean;
}

/**
 * One navigational link of the shell, with its visibility contract.
 *
 * A link with neither flag is public chrome (password recovery, email
 * confirmation): it stays reachable in both states because its address is
 * stable and the server, not the chrome, decides who may use it.
 */
export interface ShellLink {
  readonly href: string;
  /** Shown to visitors only (sign-in, registration): hidden once signed in. */
  readonly guestOnly?: boolean;
  /** Shown once signed in only (ending the session): hidden for visitors. */
  readonly requiresAuth?: boolean;
}

/**
 * visibleShellLinks chooses the links the chrome emphasizes for one session.
 *
 * It filters, never authorizes: a filtered link keeps its address, and the
 * server answers that address exactly as before.
 */
export function visibleShellLinks(
  links: readonly ShellLink[],
  session: ShellSession,
): readonly ShellLink[] {
  return links.filter((link) => {
    if (link.guestOnly === true && session.signedIn) {
      return false;
    }
    if (link.requiresAuth === true && !session.signedIn) {
      return false;
    }
    return true;
  });
}

/**
 * normalizeShellPath returns the stable form of one address: no query, no
 * fragment, rooted and without a trailing slash (except the root itself).
 * An empty address is the root: a deep link and the entry agree on it.
 */
export function normalizeShellPath(pathname: string): string {
  const bare = (pathname.split(/[?#]/)[0] ?? "").trim();
  if (bare === "" || bare === "/") {
    return "/";
  }
  const rooted = bare.startsWith("/") ? bare : `/${bare}`;
  return rooted.length > 1 && rooted.endsWith("/") ? rooted.slice(0, -1) : rooted;
}

/**
 * isCurrentShellLink answers whether a navigation href names the address the
 * visitor is on. Both sides are normalized, so `/login/` and `/login` mark
 * the same link, while `/reset` never marks `/reset/confirm`.
 */
export function isCurrentShellLink(href: string, pathname: string): boolean {
  if (href.trim() === "") {
    return false;
  }
  return normalizeShellPath(href) === normalizeShellPath(pathname);
}

/** What a status code tells the shell about the session. */
export type SessionSignal = "expired" | null;

/**
 * sessionSignalFromStatus reads the session signal of one answer: a 401
 * outside a tolerated endpoint means the session the browser held is gone,
 * and the shell must offer the entry again. Anything else is not a session
 * event, including an absent status (the request never left).
 */
export function sessionSignalFromStatus(status: number | null): SessionSignal {
  return status === 401 ? "expired" : null;
}

/**
 * signInTarget is the address an expired session returns to. It is the same
 * `/login` the server redirects to, kept in one place so the two cannot
 * drift apart.
 */
export function signInTarget(): string {
  return SIGN_IN_HREF;
}

/**
 * shellCachePolicy answers how one address may be kept: account chrome is
 * never stored (a former account's document must not be replayed to the next
 * visitor), everything else keeps the HTTP cache and its ETags.
 */
export function shellCachePolicy(path: string): "no-store" | "default" {
  const normalized = normalizeShellPath(path);
  for (const prefix of PRIVATE_PATH_PREFIXES) {
    if (normalized === prefix || normalized.startsWith(`${prefix}/`)) {
      return "no-store";
    }
  }
  return "default";
}

/** Account-scoped addresses, never cached. */
const PRIVATE_PATH_PREFIXES: readonly string[] = [
  "/login",
  "/logout",
  "/register",
  "/reset",
  "/verify",
  "/api/v1/me",
];

/** Whether the back-forward-cache listener of this document is installed. */
let pageShowInstalled = false;

/**
 * Marks the navigation link of the current address, clearing the rest. Links
 * the document does not carry (a refusal page keeps the same chrome with
 * fewer links) are simply not marked: absence is never an error.
 */
function markCurrent(document: Document, pathname: string): void {
  const current = normalizeShellPath(pathname);
  for (const anchor of document.querySelectorAll("header nav a[href]")) {
    if (!(anchor instanceof HTMLAnchorElement)) {
      continue;
    }
    const href = anchor.getAttribute("href") ?? "";
    if (href !== "" && isCurrentShellLink(href, current)) {
      anchor.setAttribute(CURRENT_ATTRIBUTE, CURRENT_VALUE);
    } else {
      anchor.removeAttribute(CURRENT_ATTRIBUTE);
    }
  }
}

/** The address the browser shows, or the root where there is no browser. */
function currentPathname(): string {
  const location = globalThis.location;
  if (typeof location === "undefined") {
    return "/";
  }
  return location.pathname;
}

/**
 * installShell marks the current navigation link of the page and keeps it
 * marked across back-forward-cache restores. Installation is idempotent per
 * document: marking runs on every call, while the marker and the single
 * `pageshow` listener are installed once, so two entries of the same page
 * never mark twice or listen twice.
 */
export function installShell(
  document: Document = globalThis.document,
  pathname: string = currentPathname(),
): void {
  markCurrent(document, pathname);
  if (document.documentElement.hasAttribute(SHELL_GUARD_ATTRIBUTE)) {
    return;
  }
  document.documentElement.setAttribute(SHELL_GUARD_ATTRIBUTE, "");
  if (pageShowInstalled) {
    return;
  }
  pageShowInstalled = true;
  globalThis.addEventListener("pageshow", (event: Event): void => {
    if (event instanceof PageTransitionEvent && event.persisted) {
      markCurrent(document, currentPathname());
    }
  });
}
