/**
 * Architectural gates of the frontend source (P18-T03, extended in P18-T04 and
 * P18-T09).
 *
 * Everything under `web/src` is served to the browser as-is, so these tests
 * read the sources and refuse: a `fetch` call outside the HTTP core, a Node
 * built-in import, unsafe rendering (`innerHTML` and friends), a UI component
 * that reaches for the translation singleton instead of receiving its text, a
 * module outside the i18n runtime that reads the catalog directly, formatting
 * that would use whatever locale the browser happens to have, and any runtime
 * dependency in `web/package.json`.
 */
import assert from "node:assert/strict";
import { test } from "node:test";
import { dirname, join } from "node:path";

import { browserSources, readPackageFile } from "./support/paths.js";

/** The single module allowed to touch the network. */
const HTTP_CORE = "src/core/http.ts";

/** Property accesses and calls banned for dynamic data. */
const UNSAFE_RENDERING: readonly RegExp[] = [
  /\.innerHTML/,
  /\.outerHTML/,
  /insertAdjacentHTML\s*\(/,
  /\beval\s*\(/,
];

/** The runtime that owns the translation catalog and the `Intl` helpers. */
const I18N_DIRECTORY = "src/i18n/";

/** The generated catalog, imported by the runtime and by nobody else. */
const TRANSLATION_CATALOG = "src/i18n/generated";

/** Module specifiers a source declares, as the import writes them. */
function importedSpecifiers(source: string): readonly string[] {
  const found: string[] = [];
  for (const match of source.matchAll(/\bfrom\s*["']([^"']+)["']|\bimport\s*["']([^"']+)["']/g)) {
    const specifier = match[1] ?? match[2];
    if (specifier !== undefined) {
      found.push(specifier);
    }
  }
  return found;
}

/**
 * importsCatalog answers whether a source reaches the generated catalog.
 *
 * The specifier is resolved against the file that writes it instead of being
 * matched as text: the runtime reaches the catalog as `./generated.js` because
 * it lives beside it, and a component reaching for the same file would write
 * `../i18n/generated.js`. Both land on the same module, so both are the same
 * claim.
 */
function importsCatalog(file: string): boolean {
  return importedSpecifiers(readPackageFile(file)).some((specifier) => {
    if (!specifier.startsWith(".")) {
      return false;
    }
    return moduleOf(join(dirname(file), specifier)) === TRANSLATION_CATALOG;
  });
}

/** moduleOf drops the extension, so a `.js` specifier matches a `.ts` source. */
function moduleOf(path: string): string {
  return path.replace(/\.(js|ts)$/, "");
}

/**
 * Calls with the implicit locale. `toLocaleString()` with no argument formats
 * with whatever locale the reader's browser was configured for, which is the
 * opposite of what a page rendered in a resolved locale needs; the i18n runtime
 * passes the locale explicitly, through `Intl`.
 */
const IMPLICIT_LOCALE: readonly RegExp[] = [
  /\btoLocaleString\s*\(\s*\)/,
  /\btoLocaleDateString\s*\(\s*\)/,
  /\btoLocaleTimeString\s*\(\s*\)/,
];

test("only the HTTP core calls fetch", () => {
  const files = browserSources();
  assert.ok(files.length >= 8, `expected to scan the frontend sources, found ${files.length}`);
  assert.ok(files.includes(HTTP_CORE), `expected ${HTTP_CORE} to be scanned`);

  const offenders = files.filter((file) => file !== HTTP_CORE && /\bfetch\s*\(/.test(readPackageFile(file)));
  assert.deepEqual(offenders, [], "components and clients must go through the HTTP core");
});

test("the browser source imports no Node built-in", () => {
  const offenders = browserSources().filter((file) => /from\s+["']node:/.test(readPackageFile(file)));
  assert.deepEqual(offenders, []);
});

test("the browser source renders dynamic data without innerHTML", () => {
  const offenders: string[] = [];
  for (const file of browserSources()) {
    const source = readPackageFile(file);
    for (const pattern of UNSAFE_RENDERING) {
      if (pattern.test(source)) {
        offenders.push(`${file} matches ${String(pattern)}`);
      }
    }
  }
  assert.deepEqual(offenders, [], "dynamic data reaches the DOM through textContent or text nodes only");
});

test("components do not import the translation singleton", () => {
  const components = browserSources().filter((file) => file.startsWith("src/components/"));
  assert.ok(components.length >= 5, `expected to scan the components, found ${components.length}`);

  const offenders = components.filter((file) => /i18n\/generated/.test(readPackageFile(file)));
  assert.deepEqual(offenders, [], "components receive their translated text, never a translation module");
});

test("every component that observes attributes handles their changes", () => {
  const components = browserSources().filter((file) => file.startsWith("src/components/"));
  assert.ok(components.length >= 5, `expected to scan the components, found ${components.length}`);

  const offenders = components.filter((file) => {
    const source = readPackageFile(file);
    return /static\s+observedAttributes/.test(source) && !/attributeChangedCallback/.test(source);
  });
  assert.deepEqual(
    offenders,
    [],
    "an observed attribute without attributeChangedCallback is an attribute the element silently ignores",
  );
});

test("only the i18n runtime reads the generated catalog", () => {
  const readers = browserSources().filter((file) => importsCatalog(file));

  assert.ok(
    readers.includes(`${I18N_DIRECTORY}translator.ts`),
    `the catalog must be read by the runtime that serves it, and the readers are: ${readers.join(", ")}`,
  );
  assert.deepEqual(
    readers.filter((file) => !file.startsWith(I18N_DIRECTORY)),
    [],
    "a page or a component receives its translated text; it does not read the catalog and decide the language itself",
  );
});

test("no delivered source formats with the browser's own locale", () => {
  const offenders: string[] = [];
  for (const file of browserSources()) {
    const source = readPackageFile(file);
    for (const pattern of IMPLICIT_LOCALE) {
      if (pattern.test(source)) {
        offenders.push(`${file} matches ${String(pattern)}`);
      }
    }
  }
  assert.deepEqual(offenders, [], "formatting passes the resolved locale explicitly or goes through the i18n runtime");
});

test("the delivered modules do not ship the comments that document the sources", () => {
  const config = JSON.parse(readPackageFile("tsconfig.json")) as {
    readonly compilerOptions?: { readonly removeComments?: boolean };
  };

  assert.equal(
    config.compilerOptions?.removeComments,
    true,
    "the compiler strips comments: they document the sources, and the browser downloads the modules",
  );
});

test("the browser bundle carries no runtime dependency", () => {
  const manifest = JSON.parse(readPackageFile("package.json")) as {
    readonly dependencies?: Readonly<Record<string, string>>;
    readonly devDependencies?: Readonly<Record<string, string>>;
  };

  assert.deepEqual(Object.keys(manifest.dependencies ?? {}), [], "runtime dependencies must stay empty");
  assert.deepEqual(
    Object.keys(manifest.devDependencies ?? {}).sort(),
    ["typescript"],
    "the build dependency is the official TypeScript package only",
  );
});
