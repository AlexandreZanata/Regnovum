/**
 * Argument publication presentation (P53-T03).
 *
 * The publication form has two surfaces with one contract: the JSON
 * call (`publishArgument`) the script speaks, and the server-rendered
 * document (`submitArenaArgument`) that works without any script —
 * every form carries a real `action`, a real `method`, the attempt
 * key the page rendered and a real CSRF field, so a double
 * submission of the same document resolves the recorded argument
 * instead of debiting INK twice. What lives in this module, DOM-free
 * so the Node runner verifies it without a browser, is the
 * translated form view, the content count the author watches while
 * typing, and the failure sentences for exactly the server codes the
 * backend emits.
 *
 * The count is feedback, never enforcement: the server measures
 * grapheme clusters and debits 1 INK per cluster in the same
 * transaction, and only it refuses what is too long or unpaid. There
 * is no monetary preview here — the balance surface belongs to the
 * wallet phase (P54), and this module computes no price and shows no
 * balance. A refusal never loses the typed text: the form re-renders
 * from the unsent values it already holds.
 */
import { formatNumber } from "../i18n/formats.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";
import { relationLabel } from "./arguments.js";

/** The strict product limit the contract declares, in grapheme clusters. */
export const MAX_GRAPHEMES = 3000;

/** Relations the publication form offers, in the order it renders them. */
export const PUBLISH_RELATIONS: readonly string[] = ["support", "oppose", "context"];

/**
 * countGraphemes counts what the author sees per extended grapheme
 * cluster. The server counts the same UAX #29 clusters; where the
 * platform has no segmenter the fallback counts code points and the
 * result stays what it is — feedback, never a verdict.
 */
export function countGraphemes(text: string): number {
  const segmenter = (Intl as unknown as { Segmenter?: new () => { segment(text: string): Iterable<unknown> } })
    .Segmenter;
  if (typeof segmenter === "function") {
    return [...new segmenter().segment(text)].length;
  }
  return [...text].length;
}

/** What the author watches while typing: a count, never a verdict. */
export interface ContentFeedback {
  readonly clusters: number;
  readonly remaining: number;
  readonly tooLong: boolean;
  readonly count: string;
}

/**
 * contentFeedback projects the count of one text for one locale. The
 * ceiling travels as a hint the server already enforces: `tooLong`
 * only warns, it never blocks the submit.
 */
export function contentFeedback(translator: Translator, locale: Locale, text: string): ContentFeedback {
  const clusters = countGraphemes(text);
  return {
    clusters,
    remaining: MAX_GRAPHEMES - clusters,
    tooLong: clusters > MAX_GRAPHEMES,
    count: translator.translate("arenas.participation.field.content_hint", { max: formatNumber(locale, MAX_GRAPHEMES) }),
  };
}

/** One unsent publication the form re-renders after a refusal. */
export interface UnsentArgument {
  readonly relation: string | null;
  readonly content: string;
}

/** Everything the page renders for the publication form. */
export interface PublishFormView {
  readonly heading: string;
  readonly intro: string;
  readonly relationLabel: string;
  readonly relations: readonly { readonly value: string; readonly label: string }[];
  readonly contentLabel: string;
  readonly contentHint: string;
  /** The typed values a refusal must not lose: empty on first render. */
  readonly unsent: UnsentArgument;
  readonly submit: string;
}

/**
 * publishFormView projects the form for one translator. The relation
 * labels are the catalog's own; the content hint carries the server
 * ceiling; the unsent values travel back verbatim so a refusal keeps
 * exactly what the author typed.
 */
export function publishFormView(translator: Translator, unsent?: UnsentArgument): PublishFormView {
  return {
    heading: translator.translate("arenas.participation.arguments.publish_heading"),
    intro: translator.translate("arenas.participation.arguments.publish_intro"),
    relationLabel: translator.translate("arenas.participation.field.relation_label"),
    relations: PUBLISH_RELATIONS.map((value) => ({ value, label: relationLabel(translator, value) })),
    contentLabel: translator.translate("arenas.participation.field.content_label"),
    contentHint: translator.translate("arenas.participation.field.content_hint", { max: MAX_GRAPHEMES }),
    unsent: unsent ?? { relation: null, content: "" },
    submit: translator.translate("arenas.participation.arguments.publish_submit"),
  };
}

/**
 * publishFailureField names the form field a refusal belongs to, so
 * the renderer can focus the error where the author fixes it. Only
 * fields the form renders are named; anything else answers null and
 * the error stays at the form.
 */
export function publishFailureField(serverCode: string): "relation" | "content" | null {
  switch (serverCode) {
    case "argument_empty_relation":
    case "argument_invalid_relation":
      return "relation";
    case "argument_empty_content":
    case "argument_invalid_content":
    case "argument_content_too_long":
    case "insufficient_ink":
      return "content";
    default:
      return null;
  }
}

/**
 * publishFailure translates a refusal by the server code the backend
 * really emits. Codes the backend never emits are not named here:
 * they fall back to the generic sentence instead of inventing a
 * meaning. No sentence prices anything: the debit belongs to the
 * server, this page only reports the refusal.
 */
export function publishFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "insufficient_ink":
      return translator.translate("arenas.participation.errors.insufficient_ink");
    case "argument_empty_content":
      return translator.translate("arenas.participation.errors.required");
    case "argument_invalid_content":
    case "argument_content_too_long":
      return translator.translate("arenas.participation.errors.invalid_content", { max: MAX_GRAPHEMES });
    case "argument_empty_relation":
    case "argument_invalid_relation":
      return translator.translate("arenas.participation.errors.invalid_relation");
    case "argument_empty_source_url":
    case "argument_invalid_source_url":
    case "argument_source_description_too_long":
      return translator.translate("arenas.participation.errors.invalid_source");
    case "arena_not_open":
      return translator.translate("arenas.participation.errors.arena_closed");
    case "account_not_eligible":
      return translator.translate("arenas.participation.errors.not_eligible");
    case "account_suspended":
      return translator.translate("arenas.participation.errors.suspended");
    case "arena_not_found":
      return translator.translate("arenas.document.not_found.detail");
    default:
      return translator.translate("arenas.arguments.failure_generic");
  }
}
