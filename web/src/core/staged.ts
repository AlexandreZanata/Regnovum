/**
 * Staged presentation capabilities (P56-T04).
 *
 * The fifteen staged operations (seasons 4, metering 4, commerce 2,
 * disputes 5) have real handlers only in the isolated Go harness
 * (`tools/stagedharness`): they are never composed into the delivered
 * process, so the browser must never treat them as an active surface.
 * These capabilities are the presentation side of that boundary.
 *
 * A capability is an allowlisted feature name handed in by composition —
 * the server composition or the test harness — never derived from the
 * DOM, the URL, storage, or any ambient browser state. There is no
 * singleton here and nothing to toggle from the console: a page
 * receives its capabilities as a value, asks `isStagedEnabled` before
 * touching a staged client, and renders the honest unavailability view
 * when the answer is no. Enabling a capability grants no authorization:
 * the server still decides authentication, ownership and state, and a
 * disabled page sends no mutation at all.
 *
 * The allowlist is closed: unknown names are dropped, duplicates
 * collapse, and the empty set — the production composition — disables
 * everything.
 */

/** The four staged presentation features, exactly the harness modules. */
export const STAGED_FEATURES = ["seasons", "metering", "commerce", "disputes"] as const;

/** One staged presentation feature. */
export type StagedFeature = (typeof STAGED_FEATURES)[number];

/** Presentation capabilities one composition hands to its staged pages. */
export interface StagedCapabilities {
  readonly enabled: ReadonlySet<StagedFeature>;
}

/**
 * stagedCapabilities builds the capabilities of one composition from
 * the allowlisted feature names it enables. Unknown names are dropped,
 * duplicates collapse, and the result is frozen: later mutation of the
 * input array changes nothing.
 */
export function stagedCapabilities(enabled: readonly string[]): StagedCapabilities {
  const accepted: StagedFeature[] = [];
  for (const name of enabled) {
    if (isStagedFeature(name) && !accepted.includes(name)) {
      accepted.push(name);
    }
  }
  return { enabled: new Set(accepted) };
}

/** noStagedCapabilities is the production composition: everything off. */
export function noStagedCapabilities(): StagedCapabilities {
  return { enabled: new Set() };
}

/** isStagedEnabled answers whether one feature is enabled in one composition. */
export function isStagedEnabled(capabilities: StagedCapabilities, feature: StagedFeature): boolean {
  return capabilities.enabled.has(feature);
}

/** isStagedFeature narrows an unknown name to the closed allowlist. */
function isStagedFeature(name: string): name is StagedFeature {
  return (STAGED_FEATURES as readonly string[]).includes(name);
}
