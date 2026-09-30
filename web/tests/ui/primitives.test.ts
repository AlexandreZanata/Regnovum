/**
 * Tests of the primitive decisions (P18-T04). The custom elements are thin
 * adapters over the functions exercised here, which is why the accessibility
 * wiring can be verified on Node without a browser.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  TOAST_DEFAULT_DURATION_MS,
  busyPresentation,
  errorSummaryItems,
  fieldWiring,
  focusTargetId,
  shouldRestoreFocus,
  summaryErrors,
  toastLiveRegion,
  toastTiming,
} from "../../src/components/primitives/model.js";
import type { ToastSeverity } from "../../src/components/primitives/model.js";

test("fieldWiring names the control and links the messages in reading order", () => {
  const wiring = fieldWiring({
    name: "email",
    label: "E-mail",
    hint: "  Use o endereço principal.  ",
    error: "Informe um e-mail válido.",
    required: true,
  });

  assert.equal(wiring.controlId, "email-control");
  assert.equal(wiring.hintId, "email-hint");
  assert.equal(wiring.errorId, "email-error");
  assert.equal(wiring.labelText, "E-mail");
  assert.deepEqual(wiring.labelAttributes, { for: "email-control" });
  assert.equal(wiring.controlAttributes["id"], "email-control");
  assert.equal(wiring.controlAttributes["aria-describedby"], "email-hint email-error");
  assert.equal(wiring.controlAttributes["aria-required"], "true");
  assert.equal(wiring.controlAttributes["aria-invalid"], "true");
  assert.deepEqual(wiring.messages, [
    { id: "email-hint", kind: "hint", text: "Use o endereço principal.", alert: false },
    { id: "email-error", kind: "error", text: "Informe um e-mail válido.", alert: true },
  ]);
});

test("fieldWiring stays quiet while a field is valid and optional", () => {
  const wiring = fieldWiring({ name: "nickname", label: "Apelido" });

  assert.equal(wiring.controlAttributes["aria-describedby"], undefined);
  assert.equal(wiring.controlAttributes["aria-required"], undefined);
  assert.equal(wiring.controlAttributes["aria-invalid"], undefined);
  assert.deepEqual(wiring.messages, []);
});

test("fieldWiring links only the error when there is no hint", () => {
  const wiring = fieldWiring({ name: "password", label: "Senha", error: "Curta demais." });

  assert.equal(wiring.controlAttributes["aria-describedby"], "password-error");
  assert.equal(wiring.controlAttributes["aria-invalid"], "true");
  assert.deepEqual(wiring.messages.map((message) => message.id), ["password-error"]);
});

test("fieldWiring ignores whitespace-only messages", () => {
  const wiring = fieldWiring({ name: "bio", label: "Bio", hint: "   ", error: "\n" });

  assert.equal(wiring.controlAttributes["aria-describedby"], undefined);
  assert.equal(wiring.controlAttributes["aria-invalid"], undefined);
  assert.deepEqual(wiring.messages, []);
});

test("fieldWiring fails closed without a translated label", () => {
  assert.throws(() => fieldWiring({ name: "email", label: "" }), /field label is required/);
  assert.throws(() => fieldWiring({ name: "email", label: "   " }), /field label is required/);
});

test("fieldWiring refuses names that are not usable identifiers", () => {
  assert.throws(() => fieldWiring({ name: "", label: "E-mail" }), /valid HTML identifier/);
  assert.throws(() => fieldWiring({ name: "e mail", label: "E-mail" }), /valid HTML identifier/);
  assert.throws(() => fieldWiring({ name: "1email", label: "E-mail" }), /valid HTML identifier/);
  assert.throws(() => fieldWiring({ name: 'a"b', label: "E-mail" }), /valid HTML identifier/);
});

test("fieldWiring accepts the identifiers the platform allows", () => {
  const wiring = fieldWiring({ name: "profile.email_v2", label: "E-mail" });
  assert.equal(wiring.controlId, "profile.email_v2-control");
});

test("errorSummaryItems keeps form order, drops blanks and lists a field once", () => {
  const items = errorSummaryItems([
    { fieldId: "email", message: "Informe um e-mail válido." },
    { fieldId: "", message: "órfã" },
    { fieldId: "username", message: "   " },
    { fieldId: "password", message: "Curta demais." },
    { fieldId: "email", message: "Segunda mensagem do mesmo campo." },
  ]);

  assert.deepEqual(items, [
    { fieldId: "email", message: "Informe um e-mail válido.", href: "#email" },
    { fieldId: "password", message: "Curta demais.", href: "#password" },
  ]);
});

test("errorSummaryItems drops a field id that cannot be a focus target", () => {
  const items = errorSummaryItems([
    { fieldId: "email", message: "Informe um e-mail válido." },
    { fieldId: "e mail", message: "com espaço" },
    { fieldId: "a#b", message: "com cerquilha" },
    { fieldId: "#", message: "fragmento vazio" },
  ]);

  assert.deepEqual(items, [{ fieldId: "email", message: "Informe um e-mail válido.", href: "#email" }]);
});

test("focusTargetId accepts only usable fragments", () => {
  assert.equal(focusTargetId("#email-control"), "email-control");
  assert.equal(focusTargetId("  #email  "), "email");
  assert.equal(focusTargetId("email"), null);
  assert.equal(focusTargetId("#"), null);
  assert.equal(focusTargetId("#email#other"), null);
  assert.equal(focusTargetId("#email other"), null);
  assert.equal(focusTargetId(""), null);
});

test("summaryErrors reads the links a server-rendered summary lists", () => {
  const errors = summaryErrors([
    { href: "#email-control", message: "Informe um e-mail válido." },
    { href: "#password-control", message: "Curta demais." },
  ]);

  assert.deepEqual(errors, [
    { fieldId: "email-control", message: "Informe um e-mail válido." },
    { fieldId: "password-control", message: "Curta demais." },
  ]);
});

test("summaryErrors drops links whose fragment is not a usable target", () => {
  const errors = summaryErrors([
    { href: "", message: "órfã" },
    { href: "email-control", message: "sem fragmento" },
    { href: "#", message: "fragmento vazio" },
    { href: "#email other", message: "com espaço" },
    { href: "#email", message: "válida" },
  ]);

  assert.deepEqual(errors, [{ fieldId: "email", message: "válida" }]);
});

test("the adopted summary keeps the document order and its normalization", () => {
  const items = errorSummaryItems(
    summaryErrors([
      { href: "#email", message: "  Informe um e-mail válido.  " },
      { href: "#", message: "órfã" },
      { href: "#password", message: "Curta demais." },
      { href: "#email", message: "Segunda mensagem do mesmo campo." },
    ]),
  );

  assert.deepEqual(items, [
    { fieldId: "email", message: "Informe um e-mail válido.", href: "#email" },
    { fieldId: "password", message: "Curta demais.", href: "#password" },
  ]);
});

test("busyPresentation marks the region and hides the decorative indicator", () => {
  const idle = busyPresentation({ busy: false, label: "Carregando" });
  assert.equal(idle.visible, false);
  assert.deepEqual(idle.hostAttributes, {});
  assert.deepEqual(idle.indicatorAttributes, { "aria-hidden": "true" });
  assert.equal(idle.statusText, "");
  assert.deepEqual(idle.statusAttributes, {});

  const busy = busyPresentation({ busy: true, label: "  Enviando…  " });
  assert.equal(busy.visible, true);
  assert.deepEqual(busy.hostAttributes, { "aria-busy": "true" });
  assert.deepEqual(busy.indicatorAttributes, { "aria-hidden": "true" });
  assert.deepEqual(busy.statusAttributes, { role: "status", "aria-live": "polite", "aria-atomic": "true" });
  assert.equal(busy.statusText, "Enviando…");

  const silent = busyPresentation({ busy: true });
  assert.equal(silent.visible, true);
  assert.equal(silent.statusText, "");
});

test("toastLiveRegion interrupts only for problems", () => {
  assert.deepEqual(toastLiveRegion("error"), { role: "alert", ariaLive: "assertive", ariaAtomic: "true" });
  assert.deepEqual(toastLiveRegion("warning"), { role: "alert", ariaLive: "assertive", ariaAtomic: "true" });
  assert.deepEqual(toastLiveRegion("info"), { role: "status", ariaLive: "polite", ariaAtomic: "true" });
  assert.deepEqual(toastLiveRegion("success"), { role: "status", ariaLive: "polite", ariaAtomic: "true" });
});

test("toastTiming never dismisses an error or a toast being read", () => {
  const idle = { focused: false, hovered: false };

  assert.deepEqual(toastTiming("success", TOAST_DEFAULT_DURATION_MS, idle), {
    dismissAfterMs: TOAST_DEFAULT_DURATION_MS,
    paused: false,
    reason: "scheduled",
  });
  assert.deepEqual(toastTiming("error", TOAST_DEFAULT_DURATION_MS, idle), {
    dismissAfterMs: null,
    paused: false,
    reason: "severity",
  });
  assert.deepEqual(toastTiming("info", TOAST_DEFAULT_DURATION_MS, { focused: true, hovered: false }), {
    dismissAfterMs: TOAST_DEFAULT_DURATION_MS,
    paused: true,
    reason: "interaction",
  });
  assert.deepEqual(toastTiming("success", TOAST_DEFAULT_DURATION_MS, { focused: false, hovered: true }), {
    dismissAfterMs: TOAST_DEFAULT_DURATION_MS,
    paused: true,
    reason: "interaction",
  });
  assert.deepEqual(toastTiming("success", 0, idle), { dismissAfterMs: null, paused: false, reason: "sticky" });
  assert.deepEqual(toastTiming("success", -1, idle), { dismissAfterMs: null, paused: false, reason: "sticky" });
  assert.deepEqual(toastTiming("success", Number.NaN, idle), { dismissAfterMs: null, paused: false, reason: "sticky" });
});

test("toastTiming covers every severity with a defined policy", () => {
  const severities: readonly ToastSeverity[] = ["info", "success", "warning", "error"];
  for (const severity of severities) {
    const timing = toastTiming(severity, TOAST_DEFAULT_DURATION_MS, { focused: false, hovered: false });
    const autoDismisses = timing.dismissAfterMs !== null;
    assert.equal(autoDismisses, severity !== "error", `severity ${severity}`);
  }
});

test("shouldRestoreFocus only moves focus the primitive took", () => {
  assert.equal(shouldRestoreFocus({ hadFocusInside: true, previousFocusConnected: true }), true);
  assert.equal(shouldRestoreFocus({ hadFocusInside: true, previousFocusConnected: false }), false);
  assert.equal(shouldRestoreFocus({ hadFocusInside: false, previousFocusConnected: true }), false);
  assert.equal(shouldRestoreFocus({ hadFocusInside: false, previousFocusConnected: false }), false);
});
