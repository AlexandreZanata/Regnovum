/**
 * Wallet client (P54-T01).
 *
 * Two safe reads and nothing else. `balance` is the owner's derived
 * bucket balances in integer INK units — never floats, never
 * pre-formatted money — and `statement` is the owner's ledger page,
 * newest first, over an opaque server-signed cursor the client never
 * parses and never builds. Both answers stay private under the
 * account no-store policy (the core derives it from the `/me/`
 * prefix, like every `/api/v1/me/*` answer).
 *
 * There is no writer here on purpose: the contract of these
 * operations is GET, and mutations only happen through the audited
 * application use cases the backend wires (checkout, portal and
 * webhooks land in P54-T03/T04). The page renders the two buckets
 * the contract declares — FREE_INK and PURCHASED_INK — and invents
 * no legacy/seasonal split: `api/openapi.json` carries no such
 * field, so any third balance would be a fiction the server could
 * not honor. The entry reference travels verbatim; there is no
 * receipt URL in the contract to follow.
 */
import type { WalletBalance, WalletStatement } from "../../contracts/generated.js";
import type { HttpCore } from "../http.js";

/** Cursor page the ledger statement reads; the cursor travels opaque. */
export interface WalletStatementQuery {
  readonly cursor?: string;
  readonly limit?: number;
}

/** Wallet reads the account views need. */
export interface WalletClient {
  /** The owner's derived bucket balances: 401 without a session. */
  balance(): Promise<WalletBalance>;
  /** One ledger page of the owner: 401 without a session. */
  statement(query?: WalletStatementQuery): Promise<WalletStatement>;
}

const WALLET_PATH = "/api/v1/me/wallet";
const STATEMENT_PATH = "/api/v1/me/wallet/transactions";

/** createWalletClient binds the wallet reads to a shared core. */
export function createWalletClient(core: HttpCore): WalletClient {
  return {
    balance: (): Promise<WalletBalance> =>
      core.request<WalletBalance>({ method: "GET", path: WALLET_PATH }),

    statement: (query?: WalletStatementQuery): Promise<WalletStatement> =>
      core.request<WalletStatement>({
        method: "GET",
        path: STATEMENT_PATH,
        ...(query === undefined ? {} : { query: { cursor: query.cursor, limit: query.limit } }),
      }),
  };
}
