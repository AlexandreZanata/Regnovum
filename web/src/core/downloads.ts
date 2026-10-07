/**
 * Downloads port (P51-T04).
 *
 * The core answers data; the file the person keeps is born here. A
 * download takes text the core already fetched over the authenticated
 * session, wraps it in a Blob of the declared media type, and exposes
 * it through an object URL the mounting component clicks — and then
 * revokes. The URL is created and revoked through this port so tests
 * replace it with a fake; the browser implementation uses only the web
 * platform (Blob, URL.createObjectURL, URL.revokeObjectURL), never a
 * library, and never a server-provided address.
 *
 * Every file this port opens must be closed: `downloadJsonFile`
 * returns the URL together with its `revoke`, and the caller revokes
 * exactly once — after the click that started the save, or when the
 * journey is abandoned. An unrevoked object URL keeps the bytes alive
 * for the lifetime of the document; for a personal export that is a
 * leak the page must not leave behind.
 */

/** The file effects a download needs, injectable for tests. */
export interface DownloadPort {
  /** Wraps bytes in an object URL of the declared media type. */
  createFileUrl(bytes: Uint8Array, mediaType: string): string;
  /** Releases an object URL this port created. */
  revokeFileUrl(url: string): void;
}

/** What the page needs to offer one file for saving. */
export interface JsonFileRequest {
  readonly filename: string;
  readonly mediaType: string;
  readonly text: string;
}

/** An opened file: the URL to click and the revoke that closes it. */
export interface OpenedFile {
  readonly url: string;
  readonly filename: string;
  /** Releases the object URL. Call exactly once. */
  revoke(): void;
}

/**
 * browserDownloadPort builds the port on the web platform. Blob and
 * URL are globals of the browser (and of the Node test runner), never
 * imports, so this module stays dependency-free.
 */
export function browserDownloadPort(): DownloadPort {
  return {
    createFileUrl: (bytes: Uint8Array, mediaType: string): string => {
      const blob = new Blob([bytes as unknown as BlobPart], { type: mediaType });
      return URL.createObjectURL(blob);
    },
    revokeFileUrl: (url: string): void => {
      URL.revokeObjectURL(url);
    },
  };
}

/**
 * downloadJsonFile opens one JSON file through the port. The text is
 * encoded once, the URL is created once, and the returned revoke
 * closes it once — the caller owns the lifetime, this function only
 * the opening.
 */
export function downloadJsonFile(port: DownloadPort, request: JsonFileRequest): OpenedFile {
  const bytes = new TextEncoder().encode(request.text);
  const url = port.createFileUrl(bytes, request.mediaType);
  let revoked = false;
  return {
    url,
    filename: request.filename,
    revoke: (): void => {
      if (revoked) {
        return;
      }
      revoked = true;
      port.revokeFileUrl(url);
    },
  };
}
