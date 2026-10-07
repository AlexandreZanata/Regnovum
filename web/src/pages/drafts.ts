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
import type { ArenaExport, PrivateArena, PrivateArenaList } from "../contracts/generated.js";
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

/** Everything the page renders for editing one saved draft. */
export interface DraftEditView {
  readonly heading: string;
  /** Names the version under edit; the save carries it back. */
  readonly versionNote: string;
  /** The server text, editable in memory only — never stored. */
  readonly statement: string;
  readonly context: string | null;
  readonly category: string;
  readonly language: string;
  /** The version the save must name, or the server refuses. */
  readonly expectedVersion: number;
  readonly submit: string;
}

/**
 * draftEditView projects one server record for editing. The text lives
 * in the view alone: unsent edits are ephemeral memory the page never
 * persists, and the version travels back so a concurrent change is
 * refused instead of overwritten.
 */
export function draftEditView(translator: Translator, draft: PrivateArena): DraftEditView {
  return {
    heading: translator.translate("arenas.drafts.edit_heading"),
    versionNote: translator.translate("arenas.drafts.edit_note", { version: draft.version }),
    statement: draft.statement,
    context: draft.context ?? null,
    category: draft.category,
    language: draft.language,
    expectedVersion: draft.version,
    submit: translator.translate("arenas.drafts.submit"),
  };
}

/** Everything the page confirms before discarding one draft. */
export interface DraftDeleteView {
  /** Names the statement and nothing else — no metadata, no reason. */
  readonly confirmation: string;
  readonly submit: string;
}

/**
 * draftDeleteView projects the sentence the page confirms with: it
 * names the draft statement the server recorded. Only drafts are
 * deletable; publishing never hides in this confirmation.
 */
export function draftDeleteView(translator: Translator, draft: PrivateArena): DraftDeleteView {
  return {
    confirmation: translator.translate("arenas.drafts.delete_confirm", { statement: draft.statement }),
    submit: translator.translate("arenas.drafts.delete_submit"),
  };
}

/** Everything the page confirms before publishing one draft. */
export interface DraftPublishView {
  /** Names the statement under publish, and nothing else. */
  readonly confirmation: string;
  /**
   * The server's cost fact: exactly one Arena Pass is consumed. The
   * browser computes no price and sends no payment; it only repeats
   * what the contract declares.
   */
  readonly costNote: string;
  readonly submit: string;
}

/**
 * draftPublishView projects the sentence the page confirms with: it
 * names the draft statement the server recorded and the single pass
 * the publication spends. A refused publish changes nothing, so the
 * confirmation promises no outcome — only the cost of trying.
 */
export function draftPublishView(translator: Translator, draft: PrivateArena): DraftPublishView {
  return {
    confirmation: translator.translate("arenas.drafts.publish_confirm", { statement: draft.statement }),
    costNote: translator.translate("arenas.drafts.publish_cost"),
    submit: translator.translate("arenas.drafts.publish_submit"),
  };
}

/** Everything the page confirms before closing one published Arena. */
export interface DraftCloseView {
  /** Names the statement under closure, and nothing else. */
  readonly confirmation: string;
  readonly submit: string;
}

/**
 * draftCloseView projects the sentence the page confirms with: it
 * names the Arena statement the server recorded. Only the creator's
 * own published Arena is closable; a stranger's reads as not found
 * and a draft as an invalid transition, both before this screen.
 */
export function draftCloseView(translator: Translator, draft: PrivateArena): DraftCloseView {
  return {
    confirmation: translator.translate("arenas.drafts.close_confirm", { statement: draft.statement }),
    submit: translator.translate("arenas.drafts.close_submit"),
  };
}

/** One public argument inside the versioned export. */
export interface ArenaExportArgumentView {
  readonly id: string;
  /** Editorial code, verbatim: the page translates no taxonomy. */
  readonly relation: string;
  /** The content, verbatim — or null when moderation withdrew it. */
  readonly content: string | null;
  readonly status: string;
}

/** Everything the page renders for one versioned public export. */
export interface ArenaExportView {
  /** The statement, verbatim: the title translates nothing. */
  readonly statement: string;
  readonly status: string;
  /** Editorial code, verbatim: the page translates no taxonomy. */
  readonly category: string;
  readonly language: string;
  readonly published: string;
  readonly participants: number;
  readonly changes: number;
  /**
   * The low-count suppression sentence when the aggregates hide,
   * else null: counts stay hidden rather than guessed.
   */
  readonly suppressedNote: string | null;
  readonly validAttributions: number;
  readonly influencedAuthors: number;
  readonly emptyArguments: string;
  readonly arguments: readonly ArenaExportArgumentView[];
  /** The opaque cursor of the next page, or null at the end. */
  readonly nextCursor: string | null;
}

/**
 * arenaExportView projects one export page for one translator. Only
 * public data renders: identity and state, aggregate counts, valid
 * influence counts and the public arguments with their sources left
 * out — sources travel in the document the downloader keeps, never
 * as rendered attribution. Withdrawn content stays null, never
 * reconstructed; the cursor is carried, never shaped.
 */
export function arenaExportView(translator: Translator, locale: Locale, doc: ArenaExport): ArenaExportView {
  return {
    statement: doc.arena.statement,
    status: translator.translate(`arenas.document.status.${doc.arena.status}`),
    category: doc.arena.category,
    language: doc.arena.language,
    published: formatInstant(locale, doc.arena.published_at, { dateStyle: "long", timeStyle: "short" }),
    participants: doc.positions.participants_total,
    changes: doc.positions.position_changes,
    suppressedNote: doc.positions.suppressed ? translator.translate("arenas.participation.aggregate.suppressed") : null,
    validAttributions: doc.influence.valid_attributions,
    influencedAuthors: doc.influence.influenced_authors,
    emptyArguments: translator.translate("arenas.drafts.export_empty"),
    arguments: doc.arguments.items.map((item) => ({
      id: item.id,
      relation: item.relation,
      content: item.content,
      status: item.status,
    })),
    nextCursor: doc.arguments.next_cursor,
  };
}

/** What the page renders when a save meets a newer version. */
export interface DraftConflictView {
  readonly note: string;
  /** The unsent text, preserved verbatim for the next attempt. */
  readonly unsentStatement: string;
  readonly unsentContext: string | null;
  readonly reload: string;
}

/**
 * draftConflictView projects a version conflict: the unsent text stays
 * on screen for the person to keep, and the only way forward is
 * re-reading the server record — never overwriting it blind.
 */
export function draftConflictView(
  translator: Translator,
  unsent: { readonly statement: string; readonly context: string | null },
): DraftConflictView {
  return {
    note: translator.translate("arenas.drafts.conflict_note"),
    unsentStatement: unsent.statement,
    unsentContext: unsent.context,
    reload: translator.translate("arenas.drafts.conflict_reload"),
  };
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
 * failureField names the form field a refusal belongs to, so the
 * renderer can focus the error where the person fixes it. Only fields
 * the form renders are named; anything else answers null and the
 * error stays at the form.
 */
export function failureField(serverCode: string): "statement" | "context" | "category" | "language" | null {
  switch (serverCode) {
    case "arena_statement_too_short":
    case "arena_statement_too_long":
    case "arena_invalid_statement":
      return "statement";
    case "arena_context_too_long":
    case "arena_invalid_context":
      return "context";
    case "arena_invalid_category":
      return "category";
    case "arena_invalid_language":
      return "language";
    default:
      return null;
  }
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
    case "version_conflict":
      return translator.translate("arenas.drafts.failure_conflict");
    case "arena_not_found":
      return translator.translate("arenas.drafts.failure_missing");
    case "no_pass_available":
      return translator.translate("arenas.drafts.failure_no_pass");
    case "invalid_status_change":
      return translator.translate("arenas.drafts.failure_status");
    case "invalid_cursor":
      return translator.translate("arenas.drafts.failure_export_page");
    default:
      return translator.translate("arenas.drafts.failure_generic");
  }
}
