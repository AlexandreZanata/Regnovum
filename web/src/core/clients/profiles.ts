/**
 * Profiles client (P51-T01).
 *
 * Three safe reads and nothing else. `mine` is the owner's private
 * projection: it answers 401 without a session and 404 until the profile
 * exists, and the core keeps it out of the browser HTTP cache like every
 * `/api/v1/me/*` answer. `byUsername` and `reputation` are public: they
 * answer 404 for an unknown or never-published author without revealing
 * anything else, and they keep the cache and its ETags.
 *
 * There is no editor here on purpose: the contract of these operations is
 * GET, and inventing a write the backend never declared would be a fiction
 * the server could not honor. The reputation document is factual counts
 * only — no score, no ranking, no attributor identity — and this client
 * renders none of those either.
 */
import type { PrivateProfile, ProfileReputation, PublicProfile } from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Read operations the account and public profile views need. */
export interface ProfilesClient {
  /** The caller's own profile: 401 without a session, 404 until created. */
  mine(): Promise<PrivateProfile>;
  /** One public profile by canonical username: 404 when unknown. */
  byUsername(username: string): Promise<PublicProfile>;
  /** The factual reputation of one author: 404 when unknown. */
  reputation(username: string): Promise<ProfileReputation>;
}

const ME_PROFILE_PATH = "/api/v1/me/profile";
const PROFILES_PATH = "/api/v1/profiles";

/** createProfilesClient binds the profile reads to a shared core. */
export function createProfilesClient(core: HttpCore): ProfilesClient {
  return {
    mine: (): Promise<PrivateProfile> => core.request<PrivateProfile>({ method: "GET", path: ME_PROFILE_PATH }),

    byUsername: (username: string): Promise<PublicProfile> =>
      core.request<PublicProfile>({ method: "GET", path: `${PROFILES_PATH}/${encodeURIComponent(username)}` }),

    reputation: (username: string): Promise<ProfileReputation> =>
      core.request<ProfileReputation>({
        method: "GET",
        path: `${PROFILES_PATH}/${encodeURIComponent(username)}/reputation`,
      }),
  };
}
