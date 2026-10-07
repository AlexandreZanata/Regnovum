/**
 * Tests of the downloads port (P51-T04).
 *
 * A fake port records every opening and closing: the file opens once
 * with the bytes and media type the page declared, and the returned
 * revoke closes it exactly once even when called twice. The browser
 * port runs against the real platform (Blob and object URLs work in
 * the Node runner too) and proves the round trip can be opened and
 * closed without a library.
 */
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  browserDownloadPort,
  downloadJsonFile,
  type DownloadPort,
} from "../../src/core/downloads.js";

/** A port that records openings and closings instead of touching files. */
function fakePort(): DownloadPort & { opened: string[]; closed: string[] } {
  const opened: string[] = [];
  const closed: string[] = [];
  let next = 0;
  return {
    opened,
    closed,
    createFileUrl: (bytes: Uint8Array, mediaType: string): string => {
      next += 1;
      opened.push(`${mediaType}:${bytes.length}`);
      return `blob:fake-${next}`;
    },
    revokeFileUrl: (url: string): void => {
      closed.push(url);
    },
  };
}

test("the file opens once with the declared bytes and closes once", () => {
  const port = fakePort();

  const file = downloadJsonFile(port, {
    filename: "personal-export-1.json",
    mediaType: "application/json",
    text: "{\"schema_version\":1}",
  });

  assert.equal(file.url, "blob:fake-1");
  assert.equal(file.filename, "personal-export-1.json");
  assert.deepEqual(port.opened, ["application/json:20"]);
  assert.deepEqual(port.closed, []);

  file.revoke();
  assert.deepEqual(port.closed, ["blob:fake-1"]);

  file.revoke();
  assert.deepEqual(port.closed, ["blob:fake-1"], "revoking twice must not close twice");
});

test("two files open and close independently", () => {
  const port = fakePort();

  const first = downloadJsonFile(port, { filename: "a.json", mediaType: "application/json", text: "{}" });
  const second = downloadJsonFile(port, { filename: "b.json", mediaType: "application/json", text: "{}" });

  assert.notEqual(first.url, second.url);
  first.revoke();
  assert.deepEqual(port.closed, [first.url]);
  second.revoke();
  assert.deepEqual(port.closed, [first.url, second.url]);
});

test("the browser port opens and closes a real object URL", () => {
  const port = browserDownloadPort();

  const file = downloadJsonFile(port, {
    filename: "personal-export-1.json",
    mediaType: "application/json",
    text: "{\"schema_version\":1}",
  });

  assert.ok(file.url.startsWith("blob:"), `unexpected url: ${file.url}`);
  file.revoke();
});
