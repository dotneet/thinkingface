import { type ApiResult, apiFetch } from "@/lib/api";
import type { FetchOpts } from "@/lib/repos";
import type { StatsResponse } from "@/types/api";

export function getStats(opts?: FetchOpts): Promise<ApiResult<StatsResponse>> {
  return apiFetch<StatsResponse>("/api/v1/stats", { headers: opts?.headers });
}
