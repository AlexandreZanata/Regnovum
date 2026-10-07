/**
 * Drafts panel presentation (P52-T03).
 *
 * The authorship panel lists the owner's private drafts and creates
 * new ones through the drafts client. What lives in this module,
 * DOM-free so the Node runner verifies it without a browser, are the
 * translated form and list views plus the single-flight guard that
 * keeps a double submit from recording twice: the client sends one
 * request per call and never replays, so the courtesy of refusing the
 * second press while one creation is in flight belongs here, beside
 * the button it protects.
 *
 * The form carries exactly the fields the contract declares —
 * statement, category, language and the optional context — and the
 * bounds travel as hints, never as client enforcement: the statement
 * is counted in runes by the server, and a browser that counted in
 * UTF-16 code units would refuse what the server accepts. No price, no
 * pass and no debit value appears anywhere: a draft never consumes an
 * Arena Pass and the browser never prices anything. Saved drafts are
 * the server's answer re-read, never an optimistic row: only a 201
 * the core confirmed joins the list.
 */
import { formatInstant } from "../i18n/formats.js";
import type { PrivateArena, PrivateArenaList } from "../contracts/generated.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";

/** Editorial categories the contract declares for a draft. */
export const DRAFT_CATEGORIES: readonly string[] = [
  "technology",
  "science",
  "philosophy",
  "politics",
  "economics",
  "health",
  "culture",
  "society",
];

/** Content languages the contract declares for a draft. */
export const DRAFT_LANGUAGES: readonly string[] = ["pt-BR", "en-US"];

/** Everything the page renders for the creation form. */
export interface DraftFormView {
  readonly heading: string;
  readonly intro: string;
  readonly statementLabel: string;
  readonly statementHint: string;
  readonly contextLabel: string;
  readonly contextHint: string;
  readonly categoryLabel: string;
  readonly categories: readonly string[];
  readonly languageLabel: string;
  readonly languages: readonly string[];
  readonly submit: string;
}

/** draftFormView projects the creation form for one translator. */
export function draftFormView(translator: Translator): DraftFormView {
  return {
    heading: translator.translate("arenas.drafts.heading"),
    intro: translator.translate("arenas.drafts.intro"),
    statementLabel: translator.translate("arenas.drafts.statement_label"),
    statementHint: translator.translate("arenas.drafts.statement_hint"),
    contextLabel: translator.translate("arenas.drafts.context_label"),
    contextHint: translator.translate("arenas.drafts.context_hint"),
    categoryLabel: translator.translate("arenas.drafts.category_label"),
    categories: DRAFT_CATEGORIES,
    languageLabel: translator.translate("arenas.drafts.language_label"),
    languages: DRAFT_LANGUAGES,
    submit: translator.translate("arenas.drafts.submit"),
  };
}

/** Everything the page renders for one saved draft row. */
export interface DraftRowView {
  readonly id: string;
  /** User content, byte-identical: never translated, never shaped. */
  readonly statement: string;
  /** Editorial code, verbatim: the page translates no taxonomy. */
  readonly category: string;
  readonly language: string;
  readonly version: number;
  readonly created: string;
  readonly status: string;
}

/** draftRow projects one saved draft for one locale. */
export function draftRow(translator: Translator, locale: Locale, draft: PrivateArena): DraftRowView {
  return {
    id: draft.id,
    statement: draft.statement,
    category: draft.category,
    language: draft.language,
    version: draft.version,
    created: formatInstant(locale, draft.created_at, { dateStyle: "medium", timeStyle: "short" }),
    status:
      draft.status === "draft"
        ? translator.translate("arenas.drafts.status_draft")
        : draft.status === "published" || draft.status === "closed" || draft.status === "restricted"
          ? translator.translate(`arenas.document.status.${draft.status}`)
          : draft.status,
  };
}

/** What the panel renders for a draft list: rows or nothing yet. */
export type DraftListView =
  | { readonly state: "ready"; readonly rows: readonly DraftRowView[] }
  | { readonly state: "empty"; readonly empty: string };

/**
 * draftListView projects one list answer for one translator. Rows are
 * the server's answer re-read: the panel never invents an optimistic
 * draft, so a creation joins the list only after its 201.
 */
export function draftListView(
  translator: Translator,
  locale: Locale,
  list: PrivateArenaList,
): DraftListView {
  if (list.items.length === 0) {
    return { state: "empty", empty: translator.translate("arenas.drafts.empty") };
  }
  return { state: "ready", rows: list.items.map((draft) => draftRow(translator, locale, draft)) };
}

/**
 * A single-flight guard for one creation: the first press while idle
 * opens the flight, any press inside it is refused, and the answer —
 * success or failure — closes it. The guard keeps no text and sends
 * nothing; it only decides whether this press may become a request.
 */
export interface CreationGuard {
  /** Opens the flight when idle; false refuses a press already flying. */
  tryBegin(): boolean;
  /** Closes the flight after the answer arrives. */
  release(): void;
}

/** createCreationGuard builds one guard, idle at first. */
export function createCreationGuard(): CreationGuard {
  let flying = false;
  return {
    tryBegin: (): boolean => {
      if (flying) {
        return false;
      }
      flying = true;
      return true;
    },
    release: (): void => {
      flying = false;
    },
  };
}

/**
 * draftFailure translates a refusal by the server code the backend
 * really emits. Codes the backend never emits are not named here: they
 * fall back to the generic sentence instead of inventing a meaning.
 */
export function draftFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "arena_statement_too_short":
    case "arena_statement_too_long":
    case "arena_context_too_long":
    case "arena_invalid_category":
    case "arena_invalid_language":
      return translator.translate("arenas.drafts.failure_invalid");
    case "rate_limited":
      return translator.translate("arenas.drafts.failure_rate_limited");
    default:
      return translator.translate("arenas.drafts.failure_generic");
  }
}
