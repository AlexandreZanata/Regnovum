/**
 * Tests of the sessions page presentation (P51-T02).
 *
 * They run the real generated catalogs in both locales: the list renders
 * allowlisted metadata only (never a token or a cookie), instants are
 * formatted instead of raw, the confirmation names the opaque target, and
 * ending the current session resolves to leaving — never to looping or to
 * an optimistic success.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  revocationConfirmation,
  revocationOutcome,
  sessionsPresentation,
} from "../../src/pages/sessions.js";
import { createTranslator } from "../../src/i18n/translator.js";
import type { SessionListResponse } from "../../src/contracts/generated.js";
import type { Locale } from "../../src/i18n/locale.js";

/** A translator holding exactly the namespaces the sessions page renders from. */
function translatorOf(locale: Locale): ReturnType<typeof createTranslator> {
  return createTranslator(locale, { namespaces: ["auth"] });
}

const LIST: SessionListResponse = {
  sessions: [
    {
      id: "sess-current",
      created_at: "2026-09-20T10:00:00Z",
      last_seen_at: "2026-09-23T10:00:00Z",
      expires_at: "2026-10-04T10:00:00Z",
      ip_address: "203.0.113.7",
      user_agent: "TestBrowser/1.0",
      current: true,
    },
    {
      id: "sess-other",
      created_at: "2026-09-21T10:00:00Z",
      last_seen_at: "2026-09-22T10:00:00Z",
      expires_at: "2026-10-05T10:00:00Z",
      current: false,
    },
  ],
};

test("the list renders in pt-BR with allowlisted metadata only", () => {
  const view = sessionsPresentation(translatorOf("pt-BR"), "pt-BR", LIST);

  assert.equal(view.heading, "Suas sessões");
  assert.equal(view.rows.length, 2);
  assert.equal(view.empty, null);
  const current = view.rows[0];
  assert.equal(current?.kind, "Esta sessão");
  assert.equal(current?.current, true);
  assert.equal(current?.address, "203.0.113.7");
  assert.equal(current?.agent, "TestBrowser/1.0");
  assert.ok(!String(current?.lastSeen).includes("2026-09-23T10:00:00Z"), "raw instant leaked");
  const other = view.rows[1];
  assert.equal(other?.kind, "Outra sessão");
  assert.equal(other?.address, null);
  assert.equal(other?.agent, null);
  const serialized = JSON.stringify(view);
  assert.ok(!serialized.includes("token"), "no token may reach the view");
  assert.ok(!serialized.includes("cookie"), "no cookie may reach the view");
});

test("the list renders in en-US and names the empty state", () => {
  const view = sessionsPresentation(translatorOf("en-US"), "en-US", LIST);
  assert.equal(view.heading, "Your sessions");

  const empty = sessionsPresentation(translatorOf("en-US"), "en-US", { sessions: [] });
  assert.deepEqual(empty.rows, []);
  assert.equal(empty.empty, "No other active session.");
});

test("the confirmation names the opaque target and nothing else", () => {
  const sentence = revocationConfirmation(translatorOf("pt-BR"), "sess-other");

  assert.ok(sentence.includes("sess-other"), `unexpected confirmation: ${sentence}`);
  assert.ok(!sentence.includes("token"), "the confirmation must not mention tokens");
});

test("ending another session re-reads the list; ending the current one leaves", () => {
  const other = revocationOutcome("sess-other", "sess-current");
  assert.deepEqual(other, { ended: "other", id: "sess-other" });

  const current = revocationOutcome("sess-current", "sess-current");
  assert.deepEqual(current, { ended: "current" });
});
