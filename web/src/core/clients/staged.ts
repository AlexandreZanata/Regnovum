/**
 * Staged module clients (P56-T04, harness-only).
 *
 * Fifteen typed operations over the four staged fragments — seasons 4,
 * metering 4, commerce 2, disputes 5 — bound to the shared HTTP core and
 * injected per page. They exist so the isolated harness (`tools/stagedharness`)
 * can exercise the real client against the real handlers; the delivered
 * process never mounts those handlers, so the production composition
 * never enables the capabilities that would call them
 * (`web/src/core/staged.ts`).
 *
 * Reads are plain GETs; the core already applies the account no-store
 * policy to every `/api/v1/me/*` path, so no cache directive is
 * assembled here. Mutations are single-shot (`retry: false`) with no
 * `Idempotency-Key`: none of the five staged POST contracts declares
 * the header, and the confirm-publication body key (`intention_key`)
 * travels in the body the fragment declares — a header alone never
 * makes a replay safe (precedent P50-T03). Path parameters travel
 * encoded; query strings are never invented.
 *
 * No Genesis, season closing, trade transfer, decree or tribunal
 * endpoint is invented here: every path, method and body below is the
 * staged fragment's own.
 */
import type { TradeReceipt, TradeStatement } from "../../contracts/staged/commerce.js";
import type { PrivateCaseFile, PrivateCaseRuling } from "../../contracts/staged/disputes.js";
import type {
  MeteringPublication,
  MeteringQuote,
  MeteringReceipt,
  MeteringStatement,
} from "../../contracts/staged/metering.js";
import type {
  SeasonChampionsDocument,
  SeasonDocument,
  SeasonHistoryDocument,
} from "../../contracts/staged/seasons.js";
import type { HttpCore } from "../http.js";

const SEASONS_PATH = "/api/v1/me/seasons";
const METERING_PATH = "/api/v1/me/metering";
const COMMERCE_PATH = "/api/v1/me/commerce";
const DISPUTES_PATH = "/api/v1/me/disputes/cases";

/** Quote a publication candidate without charging. */
export interface MeteringQuoteInput {
  readonly content: string;
  readonly service: string;
}

/** Settle one previewed candidate with its exact charge. */
export interface MeteringPublicationInput {
  readonly intention_key: string;
  readonly content: string;
  readonly service: string;
}

/** File the defense digest of one private case. */
export interface PrivateCaseDefenseInput {
  readonly digest: string;
}

/** Open the appeal of one decided private case. */
export interface PrivateCaseAppealInput {
  readonly reason: string;
}

/**
 * The fifteen staged operations the harness may exercise. Every
 * method speaks the path, method and body its fragment declares;
 * anything else is a programming error the tests refuse.
 */
export interface StagedClient {
  /** Current active season with dates and state. */
  readCurrentSeason(): Promise<SeasonDocument>;
  /** Allowlisted season history with dates and states. */
  readSeasonHistory(): Promise<SeasonHistoryDocument>;
  /** One allowlisted season with dates and state. */
  readSeason(seasonKey: string): Promise<SeasonDocument>;
  /** Privacy-safe champions of one season at cutoff. */
  readSeasonChampions(seasonKey: string): Promise<SeasonChampionsDocument>;
  /** Price one publication candidate without charging. */
  previewMeteringQuote(input: MeteringQuoteInput): Promise<MeteringQuote>;
  /** Settle one previewed candidate with its exact charge. */
  confirmMeteringPublication(input: MeteringPublicationInput): Promise<MeteringPublication>;
  /** One metering receipt with legs and optional refund. */
  readMeteringReceipt(publicationId: string): Promise<MeteringReceipt>;
  /** The metering extract with owned publication lines. */
  readMeteringStatement(): Promise<MeteringStatement>;
  /** One owned trade receipt with tithe split and compensations. */
  readTradeReceipt(contractId: string): Promise<TradeReceipt>;
  /** The participant trade extract with owned lines. */
  readTradeStatement(): Promise<TradeStatement>;
  /** One private case file the caller takes part in. */
  readPrivateCaseFile(caseKey: string): Promise<PrivateCaseFile>;
  /** Accept the sealed proposal of one private case. */
  acceptPrivateCaseTerms(caseKey: string): Promise<PrivateCaseFile>;
  /** File the defense digest of one private case. */
  filePrivateCaseDefense(caseKey: string, input: PrivateCaseDefenseInput): Promise<PrivateCaseFile>;
  /** The ruling of one decided private case. */
  readPrivateCaseRuling(caseKey: string): Promise<PrivateCaseRuling>;
  /** Open the appeal of one decided private case. */
  appealPrivateCaseRuling(caseKey: string, input: PrivateCaseAppealInput): Promise<PrivateCaseFile>;
}

/** createStagedClient binds the fifteen staged operations to a shared core. */
export function createStagedClient(core: HttpCore): StagedClient {
  return {
    readCurrentSeason: (): Promise<SeasonDocument> =>
      core.request<SeasonDocument>({ method: "GET", path: `${SEASONS_PATH}/current` }),

    readSeasonHistory: (): Promise<SeasonHistoryDocument> =>
      core.request<SeasonHistoryDocument>({ method: "GET", path: `${SEASONS_PATH}/history` }),

    readSeason: (seasonKey: string): Promise<SeasonDocument> =>
      core.request<SeasonDocument>({
        method: "GET",
        path: `${SEASONS_PATH}/${encodeURIComponent(seasonKey)}`,
      }),

    readSeasonChampions: (seasonKey: string): Promise<SeasonChampionsDocument> =>
      core.request<SeasonChampionsDocument>({
        method: "GET",
        path: `${SEASONS_PATH}/${encodeURIComponent(seasonKey)}/champions`,
      }),

    previewMeteringQuote: (input: MeteringQuoteInput): Promise<MeteringQuote> =>
      core.request<MeteringQuote>({
        method: "POST",
        path: `${METERING_PATH}/quotes`,
        body: input,
        retry: false,
      }),

    confirmMeteringPublication: (input: MeteringPublicationInput): Promise<MeteringPublication> =>
      core.request<MeteringPublication>({
        method: "POST",
        path: `${METERING_PATH}/publications`,
        body: input,
        retry: false,
      }),

    readMeteringReceipt: (publicationId: string): Promise<MeteringReceipt> =>
      core.request<MeteringReceipt>({
        method: "GET",
        path: `${METERING_PATH}/publications/${encodeURIComponent(publicationId)}`,
      }),

    readMeteringStatement: (): Promise<MeteringStatement> =>
      core.request<MeteringStatement>({ method: "GET", path: `${METERING_PATH}/statement` }),

    readTradeReceipt: (contractId: string): Promise<TradeReceipt> =>
      core.request<TradeReceipt>({
        method: "GET",
        path: `${COMMERCE_PATH}/contracts/${encodeURIComponent(contractId)}`,
      }),

    readTradeStatement: (): Promise<TradeStatement> =>
      core.request<TradeStatement>({ method: "GET", path: `${COMMERCE_PATH}/statement` }),

    readPrivateCaseFile: (caseKey: string): Promise<PrivateCaseFile> =>
      core.request<PrivateCaseFile>({
        method: "GET",
        path: `${DISPUTES_PATH}/${encodeURIComponent(caseKey)}`,
      }),

    acceptPrivateCaseTerms: (caseKey: string): Promise<PrivateCaseFile> =>
      core.request<PrivateCaseFile>({
        method: "POST",
        path: `${DISPUTES_PATH}/${encodeURIComponent(caseKey)}/accepts`,
        retry: false,
      }),

    filePrivateCaseDefense: (caseKey: string, input: PrivateCaseDefenseInput): Promise<PrivateCaseFile> =>
      core.request<PrivateCaseFile>({
        method: "POST",
        path: `${DISPUTES_PATH}/${encodeURIComponent(caseKey)}/defenses`,
        body: input,
        retry: false,
      }),

    readPrivateCaseRuling: (caseKey: string): Promise<PrivateCaseRuling> =>
      core.request<PrivateCaseRuling>({
        method: "GET",
        path: `${DISPUTES_PATH}/${encodeURIComponent(caseKey)}/ruling`,
      }),

    appealPrivateCaseRuling: (caseKey: string, input: PrivateCaseAppealInput): Promise<PrivateCaseFile> =>
      core.request<PrivateCaseFile>({
        method: "POST",
        path: `${DISPUTES_PATH}/${encodeURIComponent(caseKey)}/appeals`,
        body: input,
        retry: false,
      }),
  };
}
