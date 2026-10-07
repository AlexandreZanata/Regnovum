/**
 * Public Arena document presentation (P52-T02).
 *
 * The canonical document (`getArenaDocument`) and the participation
 * page (`showArenaParticipationPage`) are two server-rendered views of
 * one Arena: the first is the cacheable public record, the second the
 * page a person acts on. This module completes the browser side of the
 * first without duplicating anything the tree already owns: the slug
 * parsing of the participation page, the realm exhibit of the arenas
 * components, and the status and error sentences of the arenas catalog
 * all arrive by import, so there is no rival document page here — only
 * the projection the canonical address needs.
 *
 * The content language is the Arena's own: it drives the `lang` the
 * renderer writes and selects nothing else, and no hreflang is ever
 * assembled — an address per translation is a thing the contract never
 * declares. Titles, statements, contexts and categories render
 * byte-identical through text nodes downstream; the metadata of a
 * removed Arena is the gone sentence, of anything else unresolvable
 * the not-found sentence, both without echoing the requested address.
 * The document is public to visitor and holder alike: this projection
 * takes no session and branches on none.
 */
import { realmPresentation, type RealmExhibit } from "../components/arenas/model.js";
import type { PublicArena } from "../contracts/generated.js";
import { formatInstant } from "../i18n/formats.js";
import type { Locale } from "../i18n/locale.js";
import type { Translator } from "../i18n/translator.js";

/** Slug bounds the contract declares for the document address. */
const SLUG_MIN_LENGTH = 3;
const SLUG_MAX_LENGTH = 80;

/**
 * documentSlugFromPath returns the Arena slug of a canonical document
 * pathname (`/d/{slug}`), or null when the address is not one. The
 * participation address (`/arenas/{slug}`) belongs to the participation
 * module's parser; this one never claims it. Bounds are the
 * contract's: outside 3..80 the page shows not-found without fetching.
 */
export function documentSlugFromPath(pathname: string): string | null {
  const segments = pathname.split("/").filter((segment) => segment !== "");
  if (segments.length !== 2 || segments[0] !== "d") {
    return null;
  }
  const slug = segments[1] ?? "";
  if (slug.length < SLUG_MIN_LENGTH || slug.length > SLUG_MAX_LENGTH) {
    return null;
  }
  return slug;
}

/** The two server-declared addresses of one Arena. */
export interface DocumentAddresses {
  /** The canonical public document: the address caches and indexes. */
  readonly canonical: string;
  /** The page a person acts on: never cached, never indexed. */
  readonly participation: string;
}

/**
 * documentAddresses builds both addresses from one slug. Both paths
 * are the contract's — `/d/{slug}` the canonical document,
 * `/arenas/{slug}` the participation page — and the slug travels
 * encoded, never raw.
 */
export function documentAddresses(slug: string): DocumentAddresses {
  const encoded = encodeURIComponent(slug);
  return { canonical: `/d/${encoded}`, participation: `/arenas/${encoded}` };
}

/** Everything the page renders for one public Arena. */
export interface DocumentView {
  /** The statement, verbatim: the title translates nothing. */
  readonly title: string;
  /** The Arena's own content language, for the renderer's `lang`. */
  readonly contentLanguage: "pt-BR" | "en-US";
  readonly status: string;
  /** Editorial code, verbatim: the page translates no taxonomy. */
  readonly category: string;
  /** The context, verbatim, or null when the Arena carries none. */
  readonly context: string | null;
  readonly published: string;
  readonly closes: string | null;
  readonly addresses: DocumentAddresses;
  readonly realm: RealmExhibit;
}

/** documentView projects one public Arena for one locale. */
export function documentView(translator: Translator, locale: Locale, arena: PublicArena): DocumentView {
  return {
    title: arena.statement,
    contentLanguage: arena.language,
    status: translator.translate(`arenas.document.status.${arena.status}`),
    category: arena.category,
    context: arena.context,
    published: formatInstant(locale, arena.published_at, { dateStyle: "long", timeStyle: "short" }),
    closes:
      arena.closes_at === undefined || arena.closes_at === null
        ? null
        : formatInstant(locale, arena.closes_at, { dateStyle: "long", timeStyle: "short" }),
    addresses: documentAddresses(arena.slug),
    realm: realmPresentation({
      kingdom: translator.translate("arenas.realm.kingdom"),
      arena: translator.translate("arenas.realm.arena"),
      note: translator.translate("arenas.realm.no_official_outcome"),
    }),
  };
}

/** What the page renders when the address resolves to nothing usable. */
export interface DocumentErrorView {
  readonly title: string;
  readonly detail: string;
}

/**
 * documentErrorView projects a refusal for one status: removed Arenas
 * answer 410 with the gone sentence, anything else unresolvable 404
 * with not-found. Neither echoes the requested address.
 */
export function documentErrorView(translator: Translator, status: 404 | 410): DocumentErrorView {
  if (status === 410) {
    return {
      title: translator.translate("arenas.document.gone.title"),
      detail: translator.translate("arenas.document.gone.detail"),
    };
  }
  return {
    title: translator.translate("arenas.document.not_found.title"),
    detail: translator.translate("arenas.document.not_found.detail"),
  };
}
