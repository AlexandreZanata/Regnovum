/**
 * Regnovum frontend entrypoint.
 *
 * Toolchain task P01-T02: minimal native ESM entrypoint with no runtime
 * dependency. Bundlers are forbidden; `tsc` emits this module directly into
 * `web/generated/`.
 */

const appVersion: string = "dev";

export function appBanner(version: string): string {
  return `Regnovum ${version}`;
}

const banner = document.createElement("p");
banner.textContent = appBanner(appVersion);
document.body.append(banner);

export default appVersion;
