/**
 * Minimal ambient declarations for the frontend test harness (P18-T03).
 *
 * The browser runtime has zero third-party dependencies and the build keeps
 * `typescript` as its only package, so `@types/node` is not available. The
 * unit tests run on Node's built-in test runner and file system, and only the
 * exact surface used below is declared. Nothing here reaches `web/src`: these
 * declarations are compiled solely by `web/tsconfig.test.json`.
 */

declare module "node:test" {
  export function test(name: string, fn: () => void | Promise<void>): Promise<void>;
  export function describe(name: string, fn: () => void): void;
}

declare module "node:assert/strict" {
  interface Assert {
    (value: unknown, message?: string): void;
    ok(value: unknown, message?: string): void;
    equal(actual: unknown, expected: unknown, message?: string): void;
    notEqual(actual: unknown, expected: unknown, message?: string): void;
    deepEqual(actual: unknown, expected: unknown, message?: string): void;
    match(value: string, pattern: RegExp, message?: string): void;
    doesNotMatch(value: string, pattern: RegExp, message?: string): void;
    // The third shape is the constructor of the error the call must throw. The
    // i18n runtime refuses with typed errors (`MissingMessageError`,
    // `MissingPlaceholderError`, `TypeError`, `RangeError`), and asserting the
    // class is both stronger and less brittle than matching a message string.
    throws(fn: () => unknown, error?: RegExp | string | (new (...args: never[]) => object), message?: string): void;
    rejects(action: Promise<unknown> | (() => Promise<unknown>), message?: string): Promise<void>;
  }
  const assert: Assert;
  export default assert;
}

declare module "node:fs" {
  export interface GlobOptions {
    readonly cwd?: string;
  }
  export function globSync(pattern: string, options?: GlobOptions): string[];
  export function readFileSync(path: string, encoding: "utf8"): string;
  export function existsSync(path: string): boolean;
}

declare module "node:path" {
  export function join(...parts: string[]): string;
  export function dirname(path: string): string;
}

/** Node exposes its own module directory on `import.meta`. */
interface ImportMeta {
  readonly dirname: string;
  readonly filename: string;
}
