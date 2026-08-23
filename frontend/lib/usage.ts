import { type ApiResult, apiFetch } from "@/lib/api";
import type { FetchOpts } from "@/lib/repos";
import type { UsageResponse } from "@/types/api";

export function getUsage(opts?: FetchOpts): Promise<ApiResult<UsageResponse>> {
  return apiFetch<UsageResponse>("/api/v1/usage", { headers: opts?.headers });
}
