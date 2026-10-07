/**
 * Public transparency client (P55-T04).
 *
 * One public read and nothing else. `metrics` fetches the
 * versioned per-period platform metrics — suppressed integer
 * counts keyed by stable codes, with methodology version, UTC
 * bounds, timezone label and derivation instant. Bodies carry
 * counts only: no email, no provider identifier, no IP and no
 * account-level position ever serializes. The answer is public
 * with a one-hour max-age and a strong ETag, so the core keeps
 * the browser HTTP cache with its validators: no
 * `If-None-Match` is assembled by hand here, revalidation is
 * the cache's own job.
 *
 * The HTML document (`GET /transparency`) is navigation, not
 * fetch: the page links to its canonical address with
 * allowlisted parameters instead of requesting HTML as data.
 * No metric missing from the answer is inferred — a code the
 * server did not send renders no row — and no empty report is
 * ever fabricated.
 */
import type { TransparencyMetrics } from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Window the metrics read; every field travels verbatim when kept. */
export interface TransparencyQuery {
  readonly period_start?: string;
  readonly period_end?: string;
  readonly timezone?: string;
}

/** Public transparency reads the document needs. */
export interface TransparencyClient {
  /** Versioned metrics of one window: public, cacheable, anonymous. */
  metrics(query?: TransparencyQuery): Promise<TransparencyMetrics>;
}

const TRANSPARENCY_PATH = "/api/v1/public/transparency";

/** createTransparencyClient binds the public read to a shared core. */
export function createTransparencyClient(core: HttpCore): TransparencyClient {
  return {
    metrics: (query?: TransparencyQuery): Promise<TransparencyMetrics> =>
      core.request<TransparencyMetrics>({
        method: "GET",
        path: TRANSPARENCY_PATH,
        ...(query === undefined
          ? {}
          : { query: { period_start: query.period_start, period_end: query.period_end, timezone: query.timezone } }),
      }),
  };
}
