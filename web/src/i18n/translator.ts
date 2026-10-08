/**
 * The typed translator of the interface (P18-T09, I18N_STANDARD.md sections 2,
 * 3, 6 and 8).
 *
 * What this module is
 *
 * A translator is a value a page *creates* for one locale and hands to what
 * renders text. It is never a module-level singleton: nothing here is mutable
 * state shared between documents, pages or locales, so two pages of one process
 * cannot inherit each other's language, and a component that needs text receives
 * it through its own contract — the same way the primitives already receive
 * labels and messages — instead of importing a catalog and deciding for itself
 * which language the page is in (asserted by `web/tests/architecture.test.ts`).
 *
 * Typing
 *
 * The catalog is generated, so the keys are a union and the declared
 * placeholders of each key are literals. From those two facts the public API
 * derives, per key, which named values it accepts: the placeholders a message
 * declares are required properties of the call, named exactly, and a value that
 * is neither text nor a number is refused. A misspelt key, a value under the
 * wrong name and a call that omits what the message needs are therefore compile
 * errors, and the runtime makes the same refusals when the caller is not
 * TypeScript.
 *
 * One tolerance is deliberate and the type shows it: a message that declares no
 * placeholder is typed as the empty object, which any object satisfies. The
 * runtime ignores a value a message does not use, and that is what makes a
 * plural variant whose text is shorter than its base key callable with the
 * values of the base key (`plural` below).
 *
 * Placeholders
 *
 * A message is interpolated in a single pass, so a value is text and nothing
 * else: it is never read again for placeholders, which is what makes a value
 * like `{total}` a literal brace in the rendered sentence instead of a second
 * round of substitution. Values reach the DOM through `textContent`, and the
 * catalog holds no markup, so no escaping question arises here — the module
 * composes text, and the rendering boundary owns the DOM.
 *
 * Plural
 *
 * Plural selection uses `Intl.PluralRules` and *structured variants*: the
 * variants of a message are its own keys suffixed with the CLDR category —
 * `arguments.replies.one`, `arguments.replies.other` — which the catalog format
 * already expresses, because nested JSON objects are flattened into dot-joined
 * keys by the generator. The variants are additive: a message without them is
 * used as it is. No catalog message has variants today; what is proven now is
 * the mechanism, and the first message that needs them declares them where the
 * other messages live.
 */
import { messagePlaceholders, messages, namespaces } from "./generated.js";
import type { MessageKey } from "./generated.js";
import { formatNumber, pluralCategory } from "./formats.js";
import { defaultLocale } from "./locale.js";
import type { Locale } from "./locale.js";

/** The catalog namespaces, as the generator declares them. */
export type Namespace = (typeof namespaces)[number];

/** A value a message may interpolate: text, or a number to format. */
export type PlaceholderValue = string | number;

/** The placeholder names one message declares, from the generated artifact. */
type DeclaredPlaceholders<Key extends MessageKey> = (typeof messagePlaceholders)[Key][number];

/**
 * The exact values one message accepts. `translate("auth.errors.weak_password",
 * { min: 8 })` compiles and the same call without `min` does not.
 */
export type MessageValues<Key extends MessageKey> = {
  readonly [Name in DeclaredPlaceholders<Key>]: PlaceholderValue;
};

/**
 * A catalog a translator can read: the generated one, or an injected one.
 *
 * The messages are per locale; the placeholders are not, because what a message
 * needs is a property of the message and not of the language it is written in.
 * Both are injected together on purpose: the declaration of a message is what
 * the runtime enforces, so a catalog that renames a message must be able to
 * declare what it needs (which is also what the pseudo-locale of the CI gate
 * reads).
 */
export interface CatalogSource {
  readonly messages: Readonly<Record<string, Readonly<Record<string, string>>>>;
  readonly placeholders: Readonly<Record<string, readonly string[]>>;
}

/** What one translator is created from. */
export interface TranslatorOptions {
  /** The catalog to read; the generated one when omitted. */
  readonly catalog?: CatalogSource;
  /** The namespaces this translator may render; all of them when omitted. */
  readonly namespaces?: readonly Namespace[];
  /**
   * The locale to answer from when the requested one has no message for a key,
   * or null to have none. It defaults to the product's default locale, which is
   * the resilience path of I18N_STANDARD.md section 8: a page stays readable
   * instead of breaking, and the raw key is never shown to a person.
   */
  readonly fallbackLocale?: Locale | null;
}

/** A translator of one locale. */
export interface Translator {
  /** The locale this translator renders in. */
  readonly locale: Locale;
  /** The namespaces it may render. */
  readonly namespaces: readonly Namespace[];
  /** translate renders one message, or throws if it cannot. */
  translate<Key extends MessageKey>(key: Key, values?: MessageValues<Key>): string;
  /** plural renders the variant of one message the count selects. */
  plural<Key extends MessageKey>(key: Key, count: number, values?: Omit<MessageValues<Key>, "count">): string;
  /** has answers whether this translator can render a key. */
  has(key: MessageKey): boolean;
}

/** The catalog is missing a key the caller asked for. */
export class MissingMessageError extends Error {
  constructor(
    readonly key: string,
    readonly locale: Locale,
  ) {
    super(`no message for ${key} in ${locale}`);
    this.name = "MissingMessageError";
  }
}

/** A message declares a placeholder the caller did not supply. */
export class MissingPlaceholderError extends Error {
  constructor(
    readonly key: string,
    readonly placeholder: string,
  ) {
    super(`message ${key} declares {${placeholder}} and no value was supplied`);
    this.name = "MissingPlaceholderError";
  }
}

/**
 * The catalogs the build delivers. It is data, not a stateful singleton: a
 * translator copies what it needs from it at creation time, and every function
 * below accepts an injected catalog instead.
 */
const generatedCatalog: CatalogSource = { messages, placeholders: messagePlaceholders };

/**
 * Every namespace of the catalogs. The generated list rather than the key
 * table, so a new namespace is usable the day it exists without shipping
 * every key string to the browser (P58-T04: the key table cost ~3.7 KB
 * compressed on every page for nothing its runtime ever read).
 */
export const allNamespaces: readonly Namespace[] = namespaces;

/** Placeholders are `{name}`, lowercase, as the generator validates them. */
const PLACEHOLDER_PATTERN = /\{([a-z0-9_]+)\}/g;

/**
 * selectCatalog returns the messages of one locale, restricted to the requested
 * namespaces. This is the "load the catalog for a locale and a namespace" step:
 * a page that renders the account journey asks for `auth` and cannot render an
 * email subject by accident, because a key outside the requested namespaces is
 * not in the catalog it holds.
 */
export function selectCatalog(
  source: CatalogSource,
  locale: Locale,
  namespaces: readonly Namespace[] = allNamespaces,
): Readonly<Record<string, string>> {
  const selected: Record<string, string> = {};
  const localeMessages = source.messages[locale];
  if (localeMessages !== undefined) {
    for (const key of Object.keys(localeMessages)) {
      const text = localeMessages[key];
      if (text !== undefined && withinNamespaces(key, namespaces)) {
        selected[key] = text;
      }
    }
  }
  return Object.freeze(selected);
}

/** withinNamespaces answers whether a key belongs to one of the namespaces. */
function withinNamespaces(key: string, namespaces: readonly Namespace[]): boolean {
  return namespaces.some((namespace) => key.startsWith(`${namespace}.`));
}

/**
 * createTranslator builds the translator of one locale.
 *
 * A missing key is an error, never a rendered placeholder: `generate-check`
 * fails the build on locale drift, so a key that is absent here is a defect of
 * the catalog or of the caller, and both are better served by an exception than
 * by a page showing `auth.login.submit` to a person.
 */
export function createTranslator(locale: Locale, options: TranslatorOptions = {}): Translator {
  const source = options.catalog ?? generatedCatalog;
  const namespaces = options.namespaces ?? allNamespaces;
  const fallbackLocale = options.fallbackLocale === undefined ? defaultLocale : options.fallbackLocale;
  const primary = selectCatalog(source, locale, namespaces);
  const secondary =
    fallbackLocale === null || fallbackLocale === locale ? null : selectCatalog(source, fallbackLocale, namespaces);

  const textFor = (key: string): string | undefined => primary[key] ?? secondary?.[key];

  const render = (key: string, text: string, values: Readonly<Record<string, PlaceholderValue | undefined>>): string => {
    // The declaration is the authority on what a message needs, so a value the
    // caller forgot is refused before any substitution happens. The pass below
    // checks the same thing again while substituting, which is the backstop for
    // a catalog whose text and declaration disagree: a hole is never rendered.
    for (const name of source.placeholders[key] ?? []) {
      if (values[name] === undefined) {
        throw new MissingPlaceholderError(key, name);
      }
    }
    return text.replace(PLACEHOLDER_PATTERN, (_match: string, name: string) => {
      const value = values[name];
      if (value === undefined) {
        throw new MissingPlaceholderError(key, name);
      }
      // A number is formatted for the locale of the page, so a count is grouped
      // the way every other number of that document is. Text is inserted as it
      // is: it is data, and the single pass is what keeps it data.
      return typeof value === "number" ? formatNumber(locale, value) : value;
    });
  };

  return {
    locale,
    namespaces,

    translate<Key extends MessageKey>(key: Key, values?: MessageValues<Key>): string {
      const text = textFor(key);
      if (text === undefined) {
        throw new MissingMessageError(key, locale);
      }
      return render(key, text, suppliedValues(values));
    },

    plural<Key extends MessageKey>(key: Key, count: number, values?: Omit<MessageValues<Key>, "count">): string {
      // The categories are per language, so the selection is delegated to CLDR
      // through Intl: `pt-BR` selects `one` for zero and one, `en-US` selects
      // `other` for zero, and neither is a comparison to one.
      for (const variant of [`${key}.${pluralCategory(locale, count)}`, `${key}.other`, key]) {
        const text = textFor(variant);
        if (text !== undefined) {
          return render(variant, text, { ...suppliedValues(values), count });
        }
      }
      throw new MissingMessageError(key, locale);
    },

    has(key: MessageKey): boolean {
      return textFor(key) !== undefined;
    },
  };
}

/**
 * suppliedValues widens the typed values into the lookup the renderer needs.
 * The types describe the shape of a call; the renderer has to read it by name.
 */
function suppliedValues(
  values: object | undefined,
): Readonly<Record<string, PlaceholderValue | undefined>> {
  return (values ?? {}) as Readonly<Record<string, PlaceholderValue | undefined>>;
}
