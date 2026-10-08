/**
 * Metering staged client (P57-T02, harness-only).
 *
 * The four INK metering reads and writes of
 * `internal/metering/adapters/http/openapi.fragment.json`: price one
 * publication candidate without charging, settle one previewed candidate
 * with its exact charge, read one owned receipt and read the owner
 * extract. Every path, method and body below is the fragment's own: no
 * transfer, funding, release or refund endpoint is invented here.
 *
 * The preview and the confirmation are single-shot (`retry: false`) with
 * no `Idempotency-Key` header: neither POST contract declares the header,
 * and the confirmation's idempotency key (`intention_key`) travels in the
 * body the fragment declares — a header alone never makes a replay safe
 * (precedent P50-T03). A repeated confirmation of the same key replays
 * the original settlement; the same key over different terms answers
 * 409. Reads take an optional abort signal so a navigation can cancel a
 * slow answer; a cancelled answer is discarded and never renders. The
 * shared core already applies the account no-store policy to every
 * `/api/v1/me/*` path, so no cache directive is assembled here.
 *
 * The client exists so the isolated harness (`tools/stagedharness`) can
 * exercise the real client against the real handlers. The delivered
 * process never mounts those handlers, so the production composition
 * holds no capability that would call them (`web/src/core/staged.ts`
 * and `web/src/pages/metering.ts`): a disabled page renders the honest
 * unavailability view and sends nothing, never a confirmation.
 */
import type {
  MeteringPublication,
  MeteringQuote,
  MeteringReceipt,
  MeteringStatement,
} from "../../contracts/staged/metering.js";
import type { HttpCore } from "../http.js";

const METERING_PATH = "/api/v1/me/metering";

/** Price one publication candidate without charging. */
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

/** The four metering operations the harness may exercise. */
export interface MeteringClient {
  /** Price one publication candidate without charging. */
  previewMeteringQuote(input: MeteringQuoteInput): Promise<MeteringQuote>;
  /** Settle one previewed candidate with its exact charge. */
  confirmMeteringPublication(input: MeteringPublicationInput): Promise<MeteringPublication>;
  /** One owned receipt with legs and optional refund. */
  readMeteringReceipt(publicationId: string, signal?: AbortSignal): Promise<MeteringReceipt>;
  /** The owner extract with the journal-derived balance. */
  readMeteringStatement(signal?: AbortSignal): Promise<MeteringStatement>;
}

/** createMeteringClient binds the four metering operations to a shared core. */
export function createMeteringClient(core: HttpCore): MeteringClient {
  return {
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

    readMeteringReceipt: (publicationId: string, signal?: AbortSignal): Promise<MeteringReceipt> =>
      core.request<MeteringReceipt>({
        method: "GET",
        path: `${METERING_PATH}/publications/${encodeURIComponent(publicationId)}`,
        ...(signal === undefined ? {} : { signal }),
      }),

    readMeteringStatement: (signal?: AbortSignal): Promise<MeteringStatement> =>
      core.request<MeteringStatement>({
        method: "GET",
        path: `${METERING_PATH}/statement`,
        ...(signal === undefined ? {} : { signal }),
      }),
  };
}
