/**
 * Staged feature pages (P56-T04, harness-only).
 *
 * The fifteen staged operations stay off the delivered server: the
 * production composition enables no capability, so every staged page
 * renders an honest unavailability view and sends no mutation. Only
 * the isolated harness composition enables capabilities, and only
 * there does the page exercise the real staged client
 * (`web/src/core/clients/staged.ts`) against the real handlers.
 *
 * Each feature reuses its own failure catalog sentence — the same
 * title and detail the module already declares for an unavailable
 * read — so no new copy is invented and both locales render from the
 * catalog the runtime serves. The page receives its translator and
 * its capabilities as values; it reads no DOM attribute, no URL and
 * no storage to decide. Manipulating any of those never enables the
 * backend: the gate is the capabilities value alone, and enabling it
 * still grants no authorization — the server decides authentication,
 * ownership and state on every call.
 *
 * No title, market, Genesis, season closing, trade transfer, decree
 * or tribunal endpoint is invented here: unavailable means
 * unavailable, never a simulated balance, payment, decision,
 * champion or success.
 */
import { isStagedEnabled } from "../core/staged.js";
import type { StagedCapabilities, StagedFeature } from "../core/staged.js";
import type { Translator } from "../i18n/translator.js";

/** Everything one disabled staged page renders. */
export interface StagedUnavailableView {
  readonly feature: StagedFeature;
  readonly heading: string;
  readonly detail: string;
}

/** The gate of one staged page: enabled pages may call, disabled pages render. */
export interface StagedGate {
  readonly enabled: boolean;
  readonly view: StagedUnavailableView | null;
}

/** Error thrown when a disabled page is asked to call the backend. */
export class StagedUnavailableError extends Error {
  readonly feature: StagedFeature;

  constructor(feature: StagedFeature) {
    super(`staged feature ${feature} is unavailable`);
    this.name = "StagedUnavailableError";
    this.feature = feature;
  }
}

/**
 * stagedUnavailableView projects the honest unavailability sentence of
 * one staged feature for one locale. Every string comes from the
 * catalog; the view translates nothing and formats nothing itself.
 */
export function stagedUnavailableView(
  translator: Translator,
  feature: StagedFeature,
): StagedUnavailableView {
  switch (feature) {
    case "seasons":
      return {
        feature,
        heading: translator.translate("seasons.failure.title"),
        detail: translator.translate("seasons.failure.detail"),
      };
    case "metering":
      return {
        feature,
        heading: translator.translate("metering.failure.title"),
        detail: translator.translate("metering.failure.detail"),
      };
    case "commerce":
      return {
        feature,
        heading: translator.translate("commerce.failure.title"),
        detail: translator.translate("commerce.failure.detail"),
      };
    case "disputes":
      return {
        feature,
        heading: translator.translate("disputes.failure.title"),
        detail: translator.translate("disputes.failure.detail"),
      };
  }
}

/**
 * stagedGate answers one staged page: an enabled capability returns
 * the page to its caller with no view, a disabled one returns the
 * unavailability view and the caller sends nothing. The gate reads
 * only the capabilities value — never the DOM, the URL or storage.
 */
export function stagedGate(
  capabilities: StagedCapabilities,
  translator: Translator,
  feature: StagedFeature,
): StagedGate {
  if (isStagedEnabled(capabilities, feature)) {
    return { enabled: true, view: null };
  }
  return { enabled: false, view: stagedUnavailableView(translator, feature) };
}

/**
 * requireStagedEnabled guards every staged mutation: a disabled page
 * throws instead of sending. The throw carries the feature so the
 * caller renders the same unavailability view it would have rendered
 * without calling.
 */
export function requireStagedEnabled(
  capabilities: StagedCapabilities,
  feature: StagedFeature,
): void {
  if (!isStagedEnabled(capabilities, feature)) {
    throw new StagedUnavailableError(feature);
  }
}
