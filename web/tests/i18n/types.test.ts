/**
 * Type-level gates of the translator (P18-T09).
 *
 * The generated placeholder map is emitted with literal types so that a call
 * names exactly the values its message declares. That property cannot be
 * asserted at runtime and it is easy to lose by accident: one `Record<string,
 * …>` annotation on the way would turn every placeholder name into `string`,
 * make the calls below valid, and leave a translator that "works" while
 * accepting a misspelt key. The `@ts-expect-error` lines *are* the assertion —
 * if one of them stops being an error, `make typecheck` fails on the unused
 * expectation.
 *
 * What the type does not catch, and the runtime does, is stated where it
 * belongs: a call that omits a declared value is a compile error only because
 * the declared names are required properties; a call that supplies a value the
 * message does not use is tolerated on purpose, because a plural variant may
 * phrase its text with fewer values than the base key it belongs to.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { createLocalization } from "../../src/i18n/localization.js";
import { createTranslator } from "../../src/i18n/translator.js";

/**
 * Every call below is wrong, and none of them runs: the function is never
 * invoked. It exists to be compiled.
 */
export function callsTheCompilerMustRefuse(): void {
  const translator = createTranslator("pt-BR");

  // @ts-expect-error the key is not in the generated catalog
  translator.translate("auth.login.submitt");
  // @ts-expect-error the message declares {min}, not {minimum}
  translator.translate("auth.errors.weak_password", { minimum: 8 });
  // @ts-expect-error a value is text or a number, never a boolean
  translator.translate("arenas.document.page_title", { subject: true });
  // @ts-expect-error the values are an object of named placeholders
  translator.translate("auth.errors.weak_password", "12");
  // @ts-expect-error a plural key is a catalog key, not any string
  translator.plural("arenas.nope", 1);
  // @ts-expect-error a count is a number
  translator.plural("arenas.participation.arguments.replies", "2");
  // @ts-expect-error a namespace is one the generator declares
  createTranslator("pt-BR", { namespaces: ["accounts"] });
  // @ts-expect-error an interface locale is one the product ships
  createTranslator("pt-PT");
  // @ts-expect-error and switching to one it does not ship is refused as well
  createLocalization("pt-BR").switchTo("de-DE");
}

test("the refusals the compiler makes are made again at runtime", () => {
  const untyped = createTranslator("pt-BR") as unknown as {
    translate(key: string, values?: Record<string, unknown>): string;
  };
  const omitted = createTranslator("pt-BR") as unknown as {
    translate(key: string, values?: Record<string, unknown>): string;
  };

  // The types are a convenience for callers written in TypeScript; a caller
  // that is not gets the same refusal instead of a rendered key or a hole.
  assert.throws(() => untyped.translate("auth.login.submitt"), /no message for auth\.login\.submitt in pt-BR/);
  assert.throws(() => omitted.translate("auth.errors.weak_password", {}), /declares \{min\}/);
});

test("the declarations the type is built from are literals", () => {
  // A positive assertion of the same fact, for the reader who wants to see it
  // without reading the compiler's diagnostics: these two calls compile and the
  // checker refuses their neighbours above.
  const translator = createTranslator("en-US");

  assert.equal(translator.translate("auth.errors.weak_password", { min: 12 }), "The password must be at least 12 characters long.");
  assert.equal(translator.translate("arenas.document.page_title", { subject: "Prova" }), "Prova — Regnovum");
});
