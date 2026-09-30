/**
 * Structural tests of the native stylesheets (P18-T04).
 *
 * There is no CSS bundler and no preprocessor, so the conventions of
 * docs/STACK.md and docs/FRONTEND.md section 8 are enforced by reading the
 * sheets: stable layers, tokens instead of raw values, logical properties for
 * direction and zoom resilience, reduced motion, no remote dependency and no
 * `!important`.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import { readPackageFile } from "../support/paths.js";

const SHEETS: readonly string[] = [
  "src/styles/reset.css",
  "src/styles/tokens.css",
  "src/styles/base.css",
  "src/styles/primitives.css",
  "src/styles/auth.css",
  "src/styles/arena.css",
];

/** Cascade order fixed by docs/STACK.md. */
const CANONICAL_ORDER = "@layer reset, tokens, base, layout, components, utilities, overrides;";

/** Reads a sheet, failing loudly when it disappears. */
function sheet(path: string): string {
  const source = readPackageFile(path);
  assert.ok(source.trim().length > 0, `${path} is empty`);
  return source;
}

/** Removes comments so structural checks look at rules only. */
function withoutComments(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, "");
}

/** Custom properties defined by a sheet. */
function definedTokens(source: string): Set<string> {
  const names = new Set<string>();
  for (const match of withoutComments(source).matchAll(/(--ga-[a-z0-9-]+)\s*:/g)) {
    const name = match[1];
    if (name !== undefined) {
      names.add(name);
    }
  }
  return names;
}

/** Custom properties referenced by a sheet. */
function referencedTokens(source: string): readonly string[] {
  return [...withoutComments(source).matchAll(/var\(\s*(--ga-[a-z0-9-]+)/g)]
    .map((match) => match[1])
    .filter((name): name is string => name !== undefined);
}

test("every stylesheet keeps its rules inside a layer", () => {
  for (const path of SHEETS) {
    const source = sheet(path);
    for (const [index, line] of source.split("\n").entries()) {
      if (line.trim() === "") {
        continue;
      }
      // Rules must be inside a layer block, so every line is either indented
      // or one of the layer at-rules, a closing brace or a comment line.
      assert.match(
        line,
        /^(?:\s|@|\}|\/\*|\*)/,
        `${path}:${index + 1} declares a rule outside a layer: ${line}`,
      );
    }
    assert.match(withoutComments(source), /@layer [a-z]+\s*\{/, `${path} must open its own layer block`);
  }
});

test("the canonical cascade order is declared by the first sheet", () => {
  assert.ok(sheet("src/styles/reset.css").includes(CANONICAL_ORDER), "reset.css declares the layer order");
  assert.ok(sheet("src/styles/base.css").includes(CANONICAL_ORDER), "base.css restates the layer order");
  assert.match(sheet("src/styles/primitives.css"), /@layer components\s*\{/, "primitives live in the components layer");
});

test("stylesheets use tokens and never an undefined one", () => {
  const tokens = sheet("src/styles/tokens.css");
  const defined = definedTokens(tokens);
  assert.ok(defined.size >= 30, `expected the token sheet to define a palette, found ${defined.size}`);

  for (const path of SHEETS) {
    const source = sheet(path);
    for (const name of referencedTokens(source)) {
      assert.ok(defined.has(name), `${path} references ${name}, which tokens.css does not define`);
    }
  }
});

test("stylesheets carry no remote dependency and no !important", () => {
  for (const path of SHEETS) {
    const source = withoutComments(sheet(path));
    assert.doesNotMatch(source, /@import/, `${path} must not import another sheet`);
    assert.doesNotMatch(source, /url\(/, `${path} must not fetch a remote asset`);
    assert.doesNotMatch(source, /!important/, `${path} must not escalate specificity`);
  }
});

test("primitives use logical properties only", () => {
  const css = withoutComments(sheet("src/styles/primitives.css"));

  for (const property of [
    "inline-size",
    "padding-block",
    "padding-inline",
    "margin-inline-start",
    "margin-block-start",
    "inset-inline-start",
    "inset-block-end",
    "border-block-start-color",
  ]) {
    assert.ok(css.includes(`${property}:`), `primitives must use the logical property ${property}`);
  }

  assert.doesNotMatch(css, /(?:^|[^-\w])(?:margin|padding)-(?:left|right)\s*:/m, "no physical margin/padding");
  assert.doesNotMatch(css, /(?:^|[^-\w])(?:left|top|right|bottom)\s*:/m, "no physical offsets");
});

test("primitives stay readable at 200% zoom", () => {
  const css = withoutComments(sheet("src/styles/primitives.css"));

  for (const match of css.matchAll(/(?:^|[;{\s])(font-size|block-size|inline-size|height|width)\s*:\s*([^;}]+)/g)) {
    const declaration = match[2] ?? "";
    assert.doesNotMatch(
      declaration,
      /\d+px/,
      `zoomed text must scale: ${match[1]}: ${declaration.trim()}`,
    );
  }
  assert.match(css, /overflow-wrap\s*:/, "long words must wrap instead of overflowing the viewport");
});

test("primitives remove their animation under reduced motion", () => {
  const css = withoutComments(sheet("src/styles/primitives.css"));
  const reducedMotion = /@media \(prefers-reduced-motion: reduce\)\s*\{([\s\S]*?)\n\s*\}/.exec(css);

  assert.notEqual(reducedMotion, null, "primitives must honour prefers-reduced-motion");
  assert.match(reducedMotion?.[1] ?? "", /animation:\s*none/, "the reduced-motion block must stop the animation");
});

test("the local choice shows the chosen button, in forced colors too", () => {
  const css = withoutComments(sheet("src/styles/arena.css"));

  assert.match(
    css,
    /\.ga-arena__choices \[aria-pressed="true"\]/,
    "the chosen local choice must have a rule of its own",
  );
  const forcedColors = /@media \(forced-colors: active\)\s*\{([\s\S]*?)\n\s*\}/.exec(css);
  assert.notEqual(forcedColors, null, "arena.css must carry a forced-colors block");
  assert.match(
    forcedColors?.[1] ?? "",
    /\[aria-pressed="true"\]/,
    "the chosen state must stay distinguishable when the palette is forced",
  );
});

test("the token sheet publishes the motion and zoom tokens the primitives rely on", () => {
  const defined = definedTokens(sheet("src/styles/tokens.css"));

  for (const name of ["--ga-duration-fast", "--ga-duration-normal", "--ga-control-max-inline-size"]) {
    assert.ok(defined.has(name), `tokens.css must define ${name}`);
  }
  assert.match(
    sheet("src/styles/tokens.css"),
    /@media \(prefers-reduced-motion: reduce\)[\s\S]*--ga-duration-fast:\s*0ms/,
    "reduced motion must collapse the motion tokens",
  );
});
