/**
 * The browser's address, as the web UI's own server sees it, for the API's
 * rate limiter (docs/dev/agent-features.md §1.2).
 *
 * Behind the same-origin API proxy every browser request reaches the API from
 * this server, so the API cannot tell one visitor from another — and its
 * password rate limiter would put every browser in one bucket, letting a single
 * visitor who keeps failing to sign in lock everybody else out. The custom
 * server (`server.ts`) is the one place that sees the socket, so it pins the
 * answer in {@link CLIENT_ADDR_HEADER} before Next.js handles the request, and
 * the proxy forwards it (`forwardRequestHeaders` in `lib/api-proxy.ts`). The
 * API believes the header only from a peer in `TF_TRUSTED_WEB_PROXIES` or with
 * `TF_WEB_PROXY_SECRET` (`backend/internal/api/webproxy.go`).
 *
 * Framework-free and imported by `server.ts` with a relative path, so Bun runs
 * it in the production image without Next.js' path aliases.
 */

/** The browser's address, set by `server.ts` and trusted by the API only from the web tier. */
export const CLIENT_ADDR_HEADER = "x-tf-client-addr";
/** Carries `TF_WEB_PROXY_SECRET` to the API. */
export const PROXY_SECRET_HEADER = "x-tf-proxy-secret";
/**
 * Set by `server.ts` on its own process, so the proxy knows the client address
 * header was written by that server and not by the client (`next dev` and
 * `next start` never set it, and then the header is dropped instead).
 */
export const WEB_EDGE_ENV = "TF_WEB_EDGE";

/**
 * `TF_WEB_TRUSTED_PROXY_HOPS`: how many reverse proxies in front of the web UI
 * append to `X-Forwarded-For`. Zero (the default) means the socket peer is the
 * browser. Anything that is not a non-negative integer is zero — a typo must
 * not make a forged header trusted.
 */
export function trustedHops(raw: string | undefined): number {
  if (!raw || !/^\d+$/.test(raw.trim())) return 0;
  return Number.parseInt(raw.trim(), 10);
}

/**
 * The browser's address. With no trusted proxy in front it is the socket peer,
 * whatever the request claims. Behind `hops` appending proxies it is the entry
 * `hops` places from the right of `X-Forwarded-For` plus the peer — the same
 * right-to-left rule the API applies to `TF_TRUSTED_PROXY_HOPS`, since the
 * left end of the header is whatever the client typed. A chain shorter than
 * that did not come through those proxies, and the peer is the answer.
 */
export function resolveClientAddr(
  forwardedFor: string | string[] | undefined,
  peer: string | undefined,
  hops: number,
): string | undefined {
  const socketPeer = peer?.trim() || undefined;
  if (hops <= 0) return socketPeer;
  const lines = Array.isArray(forwardedFor) ? forwardedFor : forwardedFor ? [forwardedFor] : [];
  const chain = lines
    .flatMap((line) => line.split(","))
    .map((entry) => entry.trim())
    .filter((entry) => entry !== "");
  if (socketPeer) chain.push(socketPeer);
  const index = chain.length - 1 - hops;
  if (index < 0) return socketPeer;
  return chain[index];
}

/**
 * Rewrite a Node.js request's headers in place: whatever the client sent in
 * {@link CLIENT_ADDR_HEADER} or {@link PROXY_SECRET_HEADER} is discarded, and
 * the address this server resolved is set.
 */
export function pinClientAddr(
  headers: Record<string, string | string[] | undefined>,
  peer: string | undefined,
  hops: number,
): void {
  delete headers[CLIENT_ADDR_HEADER];
  delete headers[PROXY_SECRET_HEADER];
  const addr = resolveClientAddr(headers["x-forwarded-for"], peer, hops);
  if (addr) headers[CLIENT_ADDR_HEADER] = addr;
}
