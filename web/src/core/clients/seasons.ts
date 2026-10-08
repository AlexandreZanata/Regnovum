/**
 * Seasons staged client (P57-T01, harness-only).
 *
 * The four season reads of `internal/seasons/adapters/http/openapi.fragment.json`:
 * the current ACTIVE season, the allowlisted history, one allowlisted season
 * and its privacy-safe champions. Every path, method and body below is the
 * fragment's own: no Genesis, closing, archiving, transfer, decree or tribunal
 * endpoint is invented here.
 *
 * Reads are plain GETs with an optional abort signal so a navigation can
 * cancel a slow answer; a cancelled answer is discarded and never renders.
 * The shared core already applies the account no-store policy to every
 * `/api/v1/me/*` path, so no cache directive is assembled here. Path
 * parameters travel encoded; query strings are never invented.
 *
 * The client exists so the isolated harness (`tools/stagedharness`) can
 * exercise the real client against the real handlers. The delivered process
 * never mounts those handlers, so the production composition holds no
 * capability that would call them (`web/src/core/staged.ts` and
 * `web/src/pages/seasons.ts`): a disabled page renders the honest
 * unavailability view and sends nothing.
 */
import type {
  SeasonChampionsDocument,
  SeasonDocument,
  SeasonHistoryDocument,
} from "../../contracts/staged/seasons.js";
import type { HttpCore } from "../http.js";

const SEASONS_PATH = "/api/v1/me/seasons";

/** The four season reads the staged surface serves. */
export interface SeasonsClient {
  /** Current ACTIVE season with dates and state. */
  readCurrentSeason(signal?: AbortSignal): Promise<SeasonDocument>;
  /** Allowlisted season history with dates and states. */
  readSeasonHistory(signal?: AbortSignal): Promise<SeasonHistoryDocument>;
  /** One allowlisted season with dates and state. */
  readSeason(seasonKey: string, signal?: AbortSignal): Promise<SeasonDocument>;
  /** Privacy-safe champions of one season at cutoff. */
  readSeasonChampions(seasonKey: string, signal?: AbortSignal): Promise<SeasonChampionsDocument>;
}

/** createSeasonsClient binds the four season reads to a shared core. */
export function createSeasonsClient(core: HttpCore): SeasonsClient {
  return {
    readCurrentSeason: (signal?: AbortSignal): Promise<SeasonDocument> =>
      core.request<SeasonDocument>({
        method: "GET",
        path: `${SEASONS_PATH}/current`,
        ...(signal === undefined ? {} : { signal }),
      }),

    readSeasonHistory: (signal?: AbortSignal): Promise<SeasonHistoryDocument> =>
      core.request<SeasonHistoryDocument>({
        method: "GET",
        path: `${SEASONS_PATH}/history`,
        ...(signal === undefined ? {} : { signal }),
      }),

    readSeason: (seasonKey: string, signal?: AbortSignal): Promise<SeasonDocument> =>
      core.request<SeasonDocument>({
        method: "GET",
        path: `${SEASONS_PATH}/${encodeURIComponent(seasonKey)}`,
        ...(signal === undefined ? {} : { signal }),
      }),

    readSeasonChampions: (seasonKey: string, signal?: AbortSignal): Promise<SeasonChampionsDocument> =>
      core.request<SeasonChampionsDocument>({
        method: "GET",
        path: `${SEASONS_PATH}/${encodeURIComponent(seasonKey)}/champions`,
        ...(signal === undefined ? {} : { signal }),
      }),
  };
}
