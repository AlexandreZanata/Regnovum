/**
 * MFA client (P51-T03).
 *
 * The owner begins a second factor enrollment, confirms it with a code
 * from the pending secret, elevates the calling session with a fresh
 * code (step-up), or spends a one-time recovery code when the
 * authenticator is gone. The secret and the recovery codes appear in
 * exactly one server answer each; this client carries them without
 * storing them — no log, no cache, no persistence — and the page renders
 * them from that answer alone.
 *
 * Every operation here changes state without a backend-proven idempotent
 * contract, so none is retried and none carries an idempotency key: a
 * spent code stays spent, and a lost response surfaces as a failure the
 * person answers deliberately (a fresh code, never a replay the core
 * invents). Step-up elevates the calling session only; it grants no
 * role, and an elevation the server no longer honors is a refusal the
 * page reports, not a state the client assumes.
 */
import type {
  MFAConfirmRequest,
  MFAConfirmResponse,
  MFAElevationResponse,
  MFAEnrollmentResponse,
  MFAVerifyRequest,
} from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Second factor operations the account security journey needs. */
export interface MFAClient {
  /**
   * Begins an enrollment. Returns the pending secret with its otpauth
   * URI exactly once; a confirmed enrollment is refused (409), never
   * replaced.
   */
  begin(): Promise<MFAEnrollmentResponse>;
  /**
   * Confirms the pending enrollment with one of its codes. Returns the
   * one-time recovery codes in their only clear-text appearance; the
   * confirming code is spent and cannot be replayed at step-up.
   */
  confirm(input: MFAConfirmRequest): Promise<MFAConfirmResponse>;
  /**
   * Elevates the calling session with a fresh second factor code. The
   * accepted time step is spent, so a replay is refused even inside the
   * tolerated clock window.
   */
  stepUp(input: MFAVerifyRequest): Promise<MFAElevationResponse>;
  /**
   * Spends one backup code and elevates the calling session. Only a
   * code that was never spent can be spent.
   */
  recover(input: MFAVerifyRequest): Promise<MFAElevationResponse>;
}

const MFA_PATH = "/api/v1/me/mfa";

/** createMFAClient binds the second factor operations to a shared core. */
export function createMFAClient(core: HttpCore): MFAClient {
  return {
    begin: (): Promise<MFAEnrollmentResponse> =>
      core.request<MFAEnrollmentResponse>({
        method: "POST",
        path: `${MFA_PATH}/enrollment`,
        retry: false,
      }),

    confirm: (input: MFAConfirmRequest): Promise<MFAConfirmResponse> =>
      core.request<MFAConfirmResponse>({
        method: "POST",
        path: `${MFA_PATH}/enrollment/confirm`,
        body: input,
        retry: false,
      }),

    stepUp: (input: MFAVerifyRequest): Promise<MFAElevationResponse> =>
      core.request<MFAElevationResponse>({
        method: "POST",
        path: `${MFA_PATH}/step-up`,
        body: input,
        retry: false,
      }),

    recover: (input: MFAVerifyRequest): Promise<MFAElevationResponse> =>
      core.request<MFAElevationResponse>({
        method: "POST",
        path: `${MFA_PATH}/recovery`,
        body: input,
        retry: false,
      }),
  };
}
