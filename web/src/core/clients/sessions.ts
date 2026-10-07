/**
 * Sessions client (P51-T02).
 *
 * The owner lists their own sessions, rotates the calling one, and revokes
 * another one by its opaque identifier. Rotation ends the calling session
 * and opens a fresh one atomically from the caller's view; revocation of
 * the current session is the sign-out path, not a shortcut around it.
 * Revocation re-authenticates with the account password, because ending a
 * session from a stolen browser must not be one click away.
 *
 * No token ever travels here: the session lives in the `HttpOnly` cookie
 * the server sets, identifiers are opaque (never the token), and the page
 * renders metadata only — address, user agent and instants the session
 * recorded. Mutations are never retried: rotation and revocation change
 * state without a backend-proven idempotent contract, so a lost response
 * surfaces as a failure the person confirms deliberately, never as a
 * replay the core invents.
 */
import type {
  SessionListResponse,
  SessionRevocationRequest,
  SessionRevocationResponse,
  SessionRotationResponse,
} from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Session operations the account security journey needs. */
export interface SessionsClient {
  /** The owner's sessions, with the calling one marked current. */
  list(): Promise<SessionListResponse>;
  /** Ends the calling session and opens a fresh one. */
  rotate(): Promise<SessionRotationResponse>;
  /**
   * Ends one session by identifier. The password re-authenticates the
   * owner; a foreign identifier is refused exactly like a missing one.
   */
  revoke(input: SessionRevocationRequest): Promise<SessionRevocationResponse>;
}

const SESSIONS_PATH = "/api/v1/me/sessions";

/** createSessionsClient binds the session operations to a shared core. */
export function createSessionsClient(core: HttpCore): SessionsClient {
  return {
    list: (): Promise<SessionListResponse> =>
      core.request<SessionListResponse>({ method: "GET", path: SESSIONS_PATH }),

    rotate: (): Promise<SessionRotationResponse> =>
      core.request<SessionRotationResponse>({
        method: "POST",
        path: `${SESSIONS_PATH}/rotation`,
        retry: false,
      }),

    revoke: (input: SessionRevocationRequest): Promise<SessionRevocationResponse> =>
      core.request<SessionRevocationResponse>({
        method: "POST",
        path: `${SESSIONS_PATH}/revocation`,
        body: input,
        retry: false,
      }),
  };
}
