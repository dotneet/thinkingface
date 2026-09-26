/**
 * Same-origin proxy for dataset file downloads (`/datasets/{ns}/{name}/resolve/...`),
 * the `resolve` half of `app/api/[...path]/route.ts` — read that file's header
 * for why this path is proxied and how the browser ends up here.
 */
import { proxyRequest } from "@/lib/api-proxy";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

function handle(request: Request): Promise<Response> {
  return proxyRequest(request);
}

export { handle as GET, handle as HEAD };
