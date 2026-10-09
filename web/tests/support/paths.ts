/**
 * Path resolution of the frontend test suite (P18-T04).
 *
 * The tests are compiled into `web/test-build/tests/**`, so the package root is
 * resolved from this module's own location instead of the current working
 * directory: the suite then behaves the same whether it runs from `web/` (npm),
 * from the repository root (`node --test web/test-build/...`) or from CI.
 */
import { globSync, readFileSync } from "node:fs";
import { join } from "node:path";

/** Absolute path of the `web/` package. */
export const webRoot: string = join(import.meta.dirname, "..", "..", "..");

/** Reads a file of the package as UTF-8 text. */
export function readPackageFile(relativePath: string): string {
  return readFileSync(join(webRoot, relativePath), "utf8");
}

/** Every browser-facing source file of the package, sorted for stable diffs. */
export function browserSources(): readonly string[] {
  return globSync("src/**/*.ts", { cwd: webRoot }).sort();
}

/** Every test source file of the package, sorted for stable diffs. */
export function testSources(): readonly string[] {
  return globSync("tests/**/*.ts", { cwd: webRoot }).sort();
}

/** Every TypeScript source file of the package, sorted for stable diffs. */
export function allSources(): readonly string[] {
  return [...browserSources(), ...testSources()].sort();
}
