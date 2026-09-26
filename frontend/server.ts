/**
 * The production entry point of the web container (`bun run start`): Next.js
 * behind a plain HTTP server, for one reason — this is the only code that sees
 * the browser's socket. It pins the browser's address into a header the
 * same-origin API proxy forwards to the API's rate limiter (`lib/client-addr.ts`,
 * docs/dev/agent-features.md §1.2). Next.js itself only fills in
 * `X-Forwarded-For` when a client sent none, so without this the API could not
 * tell a real address from a forged one.
 *
 * `make dev-web` still runs `next dev`; there the header is simply not sent
 * and the API sees the dev server's address, which is fine on a laptop.
 */
import { createServer } from "node:http";

import next from "next";

import { WEB_EDGE_ENV, pinClientAddr, trustedHops } from "./lib/client-addr";

const port = Number.parseInt(process.env.PORT ?? "3000", 10);
// Every interface, like `next start`. Not $HOSTNAME: Docker sets it to the
// container id.
const hostname = "0.0.0.0";
const hops = trustedHops(process.env.TF_WEB_TRUSTED_PROXY_HOPS);

// Read by lib/api-proxy.ts in this same process: the client address header on
// an incoming request was written here, not by the client.
process.env[WEB_EDGE_ENV] = "1";

const app = next({ dev: false, hostname, port });
const handle = app.getRequestHandler();

await app.prepare();

createServer((req, res) => {
  pinClientAddr(req.headers, req.socket.remoteAddress, hops);
  void handle(req, res);
}).listen(port, hostname, () => {
  console.log(`> web UI listening on http://${hostname}:${port}`);
});
