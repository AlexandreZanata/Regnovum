/**
 * Personal export client (P51-T04).
 *
 * The owner requests a personal data export job and downloads the
 * generated document with the single-use capability the request answer
 * carries. The download URL is never taken from a server field: the
 * path is built from the contract template with the opaque export
 * identifier, and the token travels as the query parameter the contract
 * declares — there is no open redirect because there is no redirect to
 * honor, only this one address.
 *
 * Neither operation is retried and neither carries an idempotency key.
 * The request creates (or replays) a job without a backend-proven
 * idempotent contract, so a lost 202 surfaces as a failure the person
 * answers by asking again — which the server replays deliberately. The
 * download spends a single-use link, so replaying it client-side could
 * only burn the capability or mask an expiry: a lost response is a
 * failure, and a replayed, exhausted or expired link is the server's
 * 404, never a document served twice. Both answers are private and
 * reach the core with the account no-store policy; the binary file
 * itself leaves through the downloads port, never through a log.
 */
import type { PersonalExportDocument, PersonalExportJob } from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** What the download needs: the opaque job and its single-use token. */
export interface ExportDownloadInput {
  readonly id: string;
  readonly token: string;
}

/** Personal export operations the account privacy journey needs. */
export interface ExportsClient {
  /**
   * Requests the export job. Answers 202 with the job and the
   * single-use download token, returned exactly once; a stale session
   * is refused with step_up_required, never with a document.
   */
  request(): Promise<PersonalExportJob>;
  /**
   * Downloads the generated document to its owner. Ownership and the
   * opaque token are both required; a replayed, exhausted or expired
   * link answers 404 without serving the document again.
   */
  download(input: ExportDownloadInput): Promise<PersonalExportDocument>;
}

const EXPORTS_PATH = "/api/v1/me/exports";

/** createExportsClient binds the export operations to a shared core. */
export function createExportsClient(core: HttpCore): ExportsClient {
  return {
    request: (): Promise<PersonalExportJob> =>
      core.request<PersonalExportJob>({
        method: "POST",
        path: EXPORTS_PATH,
        retry: false,
      }),

    download: (input: ExportDownloadInput): Promise<PersonalExportDocument> =>
      core.request<PersonalExportDocument>({
        method: "GET",
        path: `${EXPORTS_PATH}/${encodeURIComponent(input.id)}/download`,
        query: { token: input.token },
        retry: false,
      }),
  };
}
