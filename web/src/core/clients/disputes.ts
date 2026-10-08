/**
 * Disputes staged client (P57-T04, harness-only).
 *
 * The five private case operations of
 * `internal/disputes/adapters/http/openapi.fragment.json`: read one
 * owned case file, accept its sealed terms, file one defense digest,
 * read its stable ruling and appeal it once with an explicit reason.
 * Every path, method and body below is the fragment's own: no
 * tribunal, decree or moderation endpoint is invented here.
 *
 * Mutations are single-shot (`retry: false`) with no
 * `Idempotency-Key` header: no disputes POST contract declares the
 * header, and the defense digest and the appeal reason travel in the
 * bodies the fragment declares — a header alone never makes a replay
 * safe (precedent P50-T03). A repeated acceptance answers unchanged;
 * a second appeal over the same ruling answers 409. Reads take an
 * optional abort signal so a navigation can cancel a slow answer; a
 * cancelled answer is discarded and never renders. The shared core
 * already applies the account no-store policy to every
 * `/api/v1/me/*` path, so no cache directive is assembled here. Case
 * keys travel encoded; query strings are never invented.
 *
 * The client exists so the isolated harness (`tools/stagedharness`)
 * can exercise the real client against the real handlers. The
 * delivered process never mounts those handlers, so the production
 * composition holds no capability that would call them
 * (`web/src/core/staged.ts` and `web/src/pages/disputes.ts`): a
 * disabled page renders the honest unavailability view and sends
 * nothing.
 */
import type { PrivateCaseFile, PrivateCaseRuling } from "../../contracts/staged/disputes.js";
import type { HttpCore } from "../http.js";

const DISPUTES_PATH = "/api/v1/me/disputes/cases";

/** File the defense digest of one private case. */
export interface PrivateCaseDefenseInput {
  readonly digest: string;
}

/** Open the appeal of one decided private case. */
export interface PrivateCaseAppealInput {
  readonly reason: string;
}

/** The five private case operations the harness may exercise. */
export interface DisputesClient {
  /** One private case file the caller takes part in. */
  readPrivateCaseFile(caseKey: string, signal?: AbortSignal): Promise<PrivateCaseFile>;
  /** Accept the sealed proposal of one private case. */
  acceptPrivateCaseTerms(caseKey: string): Promise<PrivateCaseFile>;
  /** File the defense digest of one private case. */
  filePrivateCaseDefense(caseKey: string, input: PrivateCaseDefenseInput): Promise<PrivateCaseFile>;
  /** The ruling of one decided private case. */
  readPrivateCaseRuling(caseKey: string, signal?: AbortSignal): Promise<PrivateCaseRuling>;
  /** Open the appeal of one decided private case. */
  appealPrivateCaseRuling(caseKey: string, input: PrivateCaseAppealInput): Promise<PrivateCaseFile>;
}

/** createDisputesClient binds the five case operations to a shared core. */
export function createDisputesClient(core: HttpCore): DisputesClient {
  return {
    readPrivateCaseFile: (caseKey: string, signal?: AbortSignal): Promise<PrivateCaseFile> =>
      core.request<PrivateCaseFile>({
        method: "GET",
        path: `${DISPUTES_PATH}/${encodeURIComponent(caseKey)}`,
        ...(signal === undefined ? {} : { signal }),
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

    readPrivateCaseRuling: (caseKey: string, signal?: AbortSignal): Promise<PrivateCaseRuling> =>
      core.request<PrivateCaseRuling>({
        method: "GET",
        path: `${DISPUTES_PATH}/${encodeURIComponent(caseKey)}/ruling`,
        ...(signal === undefined ? {} : { signal }),
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
