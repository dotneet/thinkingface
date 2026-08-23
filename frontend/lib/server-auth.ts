import { cookies } from "next/headers";

/**
 * Server Components can't rely on `credentials: "include"` (that's a browser
 * fetch concept) to forward the tf_session cookie to the backend. This reads
 * the incoming request's cookies and turns them into a header we can attach
 * manually to server-side API calls that may need auth (write access,
 * token management, "me").
 *
 * Only import this from Server Components / route handlers.
 */
export async function authHeaders(): Promise<Record<string, string>> {
  const store = await cookies();
  const header = store.toString();
  return header ? { Cookie: header } : {};
}
