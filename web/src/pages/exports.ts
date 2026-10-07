/**
 * Exports page presentation (P51-T04).
 *
 * The page walks the owner from the request to the saved file, but it
 * decides almost nothing: the server owns the job, its readiness and
 * every refusal. What lives here, DOM-free so the Node runner verifies
 * it without a browser, are the translated views each step projects —
 * the request entry, the pending job, the ready job, and the file
 * descriptor the downloads port opens.
 *
 * Two honesties shape this module. First, there is no status endpoint
 * in the contract, so there is no polling here: a job whose status is
 * `requested` is pending, a job whose status is `ready` downloads, and
 * meeting the active job again means asking again — the only address
 * the contract declares. Second, the file descriptor carries no
 * server-provided URL and no request-controlled name: the filename is
 * built from the opaque export identifier the request answer issued,
 * the media type is the constant the contract declares
 * (`application/json`), and the bytes are the downloaded document the
 * caller hands in. The object URL itself is born and revoked in the
 * downloads port; this module only describes what to open and reminds
 * the mounting component to close it.
 */
import type { PersonalExportDocument, PersonalExportJob } from "../contracts/generated.js";
import type { Translator } from "../i18n/translator.js";

/** Everything the page renders before any job exists. */
export interface ExportRequestView {
  readonly heading: string;
  readonly intro: string;
  readonly stepUpNote: string;
  readonly submit: string;
}

/** exportRequestView projects the request entry for one translator. */
export function exportRequestView(translator: Translator): ExportRequestView {
  return {
    heading: translator.translate("auth.exports.heading"),
    intro: translator.translate("auth.exports.intro"),
    stepUpNote: translator.translate("auth.exports.stepup_note"),
    submit: translator.translate("auth.exports.request_submit"),
  };
}

/** What the page renders for a job: pending or ready, never invented. */
export type ExportJobState =
  | { readonly state: "pending"; readonly heading: string; readonly note: string }
  | { readonly state: "ready"; readonly heading: string; readonly note: string; readonly submit: string };

/**
 * exportJobView projects one job answer for one translator. Only the
 * statuses the contract declares are named; anything else is a
 * programming error, never a third state the page renders.
 */
export function exportJobView(translator: Translator, job: PersonalExportJob): ExportJobState {
  if (job.status === "ready") {
    return {
      state: "ready",
      heading: translator.translate("auth.exports.ready_heading"),
      note: translator.translate("auth.exports.ready_note"),
      submit: translator.translate("auth.exports.download_submit"),
    };
  }
  return {
    state: "pending",
    heading: translator.translate("auth.exports.pending_heading"),
    note: translator.translate("auth.exports.pending_note"),
  };
}

/** Everything the downloads port needs to open one file. */
export interface ExportFileDescriptor {
  /** Built from the opaque export identifier; never request-controlled. */
  readonly filename: string;
  /** The constant the contract declares for the document. */
  readonly mediaType: "application/json";
  /** The downloaded document, serialized once for the file. */
  readonly text: string;
  readonly revokeNote: string;
}

/**
 * exportFileDescriptor describes the file for one downloaded document.
 * The text is the document the core fetched over the authenticated
 * session — re-serialized as JSON, still the machine-readable document
 * of schema_version 1 the server served.
 */
export function exportFileDescriptor(
  translator: Translator,
  job: PersonalExportJob,
  document: PersonalExportDocument,
): ExportFileDescriptor {
  return {
    filename: `personal-export-${job.export_id}.json`,
    mediaType: "application/json",
    text: JSON.stringify(document),
    revokeNote: translator.translate("auth.exports.revoke_note"),
  };
}

/**
 * exportFailure translates a refusal by the server code the backend
 * really emits. Codes the backend never emits are not named here: they
 * fall back to the generic sentence instead of inventing a meaning.
 */
export function exportFailure(translator: Translator, serverCode: string): string {
  switch (serverCode) {
    case "step_up_required":
      return translator.translate("auth.exports.failure_stale");
    case "export_not_found":
      return translator.translate("auth.exports.failure_gone");
    case "invalid_export_token":
      return translator.translate("auth.exports.failure_forged");
    default:
      return translator.translate("auth.exports.failure_generic");
  }
}
