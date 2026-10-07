/**
 * Arenas client (P18-T03; journey P18-T06; versioned export P52-T05).
 *
 * Public reads only. Everything here is a safe method, so the core applies
 * its retry policy, and the HTTP cache keeps the ETags the backend sends.
 * The versioned export revalidates through that same cache: the page
 * never assembles an `If-None-Match` validator itself, and only public
 * data ever serializes — no individual position, change history or
 * attributor identity is part of the document.
 */
import type { ArenaExport, ArenaFeed, PublicArena, SearchArenaPage } from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Feed filters of `GET /api/v1/arenas`; cursor pages stay opaque. */
export interface ArenaFeedQuery {
  readonly language?: string;
  readonly category?: string;
  readonly status?: string;
  readonly cursor?: string;
  readonly limit?: number;
}

/** Search filters of `GET /api/v1/search/arenas`. */
export interface ArenaSearchQuery {
  readonly q: string;
  readonly language?: string;
  readonly cursor?: string;
  readonly limit?: number;
}

/** Page window of `GET /api/v1/arenas/{id}/export`; the cursor stays opaque. */
export interface ArenaExportQuery {
  readonly cursor?: string;
  readonly limit?: number;
}

/** Read operations the arena pages need. */
export interface ArenasClient {
  feed(query?: ArenaFeedQuery): Promise<ArenaFeed>;
  bySlug(slug: string): Promise<PublicArena>;
  search(query: ArenaSearchQuery): Promise<SearchArenaPage>;
  /**
   * One page of the versioned public export of one Arena by its
   * stable identifier. Unknown, draft and removed Arenas read as
   * not found. The page size travels as asked — the 1..100 bounds
   * are the server's to enforce — and the cursor is never built or
   * shaped here.
   */
  exportById(id: string, query?: ArenaExportQuery): Promise<ArenaExport>;
}

const ARENAS_PATH = "/api/v1/arenas";
const SEARCH_PATH = "/api/v1/search/arenas";

/** createArenasClient binds the public Arena reads to a shared core. */
export function createArenasClient(core: HttpCore): ArenasClient {
  return {
    feed: (query?: ArenaFeedQuery): Promise<ArenaFeed> =>
      core.request<ArenaFeed>({
        method: "GET",
        path: ARENAS_PATH,
        ...(query === undefined
          ? {}
          : { query: { language: query.language, category: query.category, status: query.status, cursor: query.cursor, limit: query.limit } }),
      }),

    bySlug: (slug: string): Promise<PublicArena> =>
      core.request<PublicArena>({ method: "GET", path: `${ARENAS_PATH}/${encodeURIComponent(slug)}` }),

    search: (query: ArenaSearchQuery): Promise<SearchArenaPage> =>
      core.request<SearchArenaPage>({
        method: "GET",
        path: SEARCH_PATH,
        query: { q: query.q, language: query.language, cursor: query.cursor, limit: query.limit },
      }),

    exportById: (id: string, query?: ArenaExportQuery): Promise<ArenaExport> =>
      core.request<ArenaExport>({
        method: "GET",
        path: `${ARENAS_PATH}/${encodeURIComponent(id)}/export`,
        ...(query === undefined ? {} : { query: { cursor: query.cursor, limit: query.limit } }),
      }),
  };
}
