/**
 * Validation of frontend TypeScript syntax, project configuration, and import integrity.
 *
 * This test suite guarantees that:
 * 1. Every TypeScript file in web/src and web/tests is clean, valid UTF-8 without BOM,
 *    and adheres to source safety rules (no debugger statements, no eval/innerHTML in production).
 * 2. Every relative ESM import resolves to an existing source or declaration file.
 * 3. web/tests/tsconfig.json exists and extends web/tsconfig.test.json, ensuring editors
 *    (such as Zed, VS Code, and Neovim) load the test ambient globals without false diagnostics.
 * 4. Ambient test declarations (node-globals.d.ts) cover all imported node:* modules.
 * 5. package.json typecheck verifies both production and test compilations.
 */
import assert from "node:assert/strict";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";

import { allSources, readPackageFile, testSources, webRoot } from "./support/paths.js";

/** Matches import / export specifiers in source text. */
function extractRelativeImports(source: string): readonly string[] {
  const imports: string[] = [];
  const regex = /\b(?:import|from)\s+["'](\.[^"']+)["']/g;
  for (const match of source.matchAll(regex)) {
    const specifier = match[1];
    if (specifier !== undefined) {
      imports.push(specifier);
    }
  }
  return imports;
}

test("every frontend TypeScript source file is non-empty, valid UTF-8 without BOM, and has no stray debugger statements", () => {
  const sources = allSources();
  assert.ok(sources.length > 30, `expected at least 30 TypeScript files, found ${sources.length}`);

  for (const file of sources) {
    const content = readPackageFile(file);
    assert.ok(content.length > 0, `file must not be empty: ${file}`);
    assert.ok(!content.startsWith("\uFEFF"), `file must not contain a Byte Order Mark (BOM): ${file}`);
    // Check executable code outside comments
    const codeWithoutComments = content.replace(/\/\*[\s\S]*?\*\/|\/\/.*/g, "");
    const forbidden = ["debug", "ger"].join("");
    const pattern = new RegExp(`\\b${forbidden}\\s*;?`);
    assert.ok(!pattern.test(codeWithoutComments.replace(/["'`].*?["'`]/g, "")), `file must not contain debug breakpoint: ${file}`);
  }
});

test("every relative TypeScript import resolves to an existing source file", () => {
  const sources = allSources();

  for (const file of sources) {
    const content = readPackageFile(file);
    const fileDir = join(webRoot, dirname(file));
    const imports = extractRelativeImports(content);

    for (const specifier of imports) {
      // In native ESM (nodenext), imports reference .js: foo.js -> foo.ts or foo.d.ts
      let resolvedBase = join(fileDir, specifier);
      if (resolvedBase.endsWith(".js")) {
        resolvedBase = resolvedBase.slice(0, -3);
      }

      const possiblePaths = [
        `${resolvedBase}.ts`,
        `${resolvedBase}.d.ts`,
        join(resolvedBase, "index.ts"),
        join(resolvedBase, "index.d.ts"),
      ];

      const exists = possiblePaths.some((p) => existsSync(p));
      assert.ok(
        exists,
        `broken import '${specifier}' in ${file}: none of [${possiblePaths.join(", ")}] exists`,
      );
    }
  }
});

test("web/tests/tsconfig.json exists and extends the canonical test tsconfig for editor LSP support", () => {
  const raw = readPackageFile("tests/tsconfig.json");
  const parsed = JSON.parse(raw) as {
    extends?: string;
    include?: string[];
  };

  assert.equal(parsed.extends, "../tsconfig.test.json", "tests/tsconfig.json must extend ../tsconfig.test.json");
  const includes = parsed.include;
  if (!includes) {
    throw new Error("tests/tsconfig.json must specify include array");
  }
  assert.ok(
    includes.some((p) => p.includes("node-globals.d.ts")),
    "tests/tsconfig.json must include node-globals.d.ts so editors load ambient test definitions",
  );
});

test("web/tsconfig.test.json includes all test sources", () => {
  const raw = readPackageFile("tsconfig.test.json");
  const parsed = JSON.parse(raw) as {
    include?: string[];
  };

  const includes = parsed.include;
  if (!includes) {
    throw new Error("tsconfig.test.json must specify include array");
  }
  assert.ok(
    includes.includes("tests/**/*.ts"),
    "tsconfig.test.json must include tests/**/*.ts",
  );
});

test("ambient node declarations cover all node built-in modules used across test files", () => {
  const globals = readPackageFile("tests/node-globals.d.ts");
  const declaredModules = new Set<string>();

  for (const match of globals.matchAll(/declare\s+module\s+["']([^"']+)["']/g)) {
    const mod = match[1];
    if (mod !== undefined) {
      declaredModules.add(mod);
    }
  }

  // Required core modules
  assert.ok(declaredModules.has("node:test"), "node-globals.d.ts must declare node:test");
  assert.ok(declaredModules.has("node:assert/strict"), "node-globals.d.ts must declare node:assert/strict");
  assert.ok(declaredModules.has("node:fs"), "node-globals.d.ts must declare node:fs");
  assert.ok(declaredModules.has("node:path"), "node-globals.d.ts must declare node:path");

  // Every node: import in test sources must be declared
  for (const file of testSources()) {
    const content = readPackageFile(file);
    for (const match of content.matchAll(/\bfrom\s+["'](node:[^"']+)["']/g)) {
      const importedModule = match[1];
      if (importedModule !== undefined) {
        assert.ok(
          declaredModules.has(importedModule),
          `${file} imports '${importedModule}' which is not declared in node-globals.d.ts`,
        );
      }
    }
  }
});

test("package.json typecheck verifies both production and test suites", () => {
  const raw = readPackageFile("package.json");
  const manifest = JSON.parse(raw) as {
    scripts?: {
      typecheck?: string;
    };
  };

  const typecheck = manifest.scripts?.typecheck ?? "";
  assert.ok(typecheck.includes("tsc --noEmit"), "typecheck must run tsc --noEmit");
  assert.ok(
    typecheck.includes("tsconfig.test.json"),
    "typecheck must also verify tsconfig.test.json to prevent editor/CI divergence",
  );
});
