import { type ApiResult, apiFetch } from "@/lib/api";
import type {
  CreateWebhookRequest,
  CreateWebhookResponse,
  UpdateWebhookRequest,
  UpdateWebhookResponse,
  Webhook,
  WebhookDelivery,
  WebhookDeliveryListResponse,
  WebhookListResponse,
} from "@/types/api";

export function listWebhooks(namespace: string): Promise<ApiResult<WebhookListResponse>> {
  return apiFetch<WebhookListResponse>(
    `/api/v1/namespaces/${encodeURIComponent(namespace)}/webhooks`,
  );
}

export function createWebhook(
  namespace: string,
  req: CreateWebhookRequest,
): Promise<ApiResult<CreateWebhookResponse>> {
  return apiFetch<CreateWebhookResponse>(
    `/api/v1/namespaces/${encodeURIComponent(namespace)}/webhooks`,
    { method: "POST", body: req },
  );
}

export function updateWebhook(
  id: number,
  req: UpdateWebhookRequest,
): Promise<ApiResult<UpdateWebhookResponse>> {
  return apiFetch<UpdateWebhookResponse>(`/api/v1/webhooks/${id}`, {
    method: "PUT",
    body: req,
  });
}

export function deleteWebhook(id: number): Promise<ApiResult<void>> {
  return apiFetch<void>(`/api/v1/webhooks/${id}`, { method: "DELETE" });
}

export function listWebhookDeliveries(
  webhookId: number,
  params?: { limit?: number; offset?: number },
): Promise<ApiResult<WebhookDeliveryListResponse>> {
  return apiFetch<WebhookDeliveryListResponse>(`/api/v1/webhooks/${webhookId}/deliveries`, {
    query: params,
  });
}

export function redeliverWebhook(
  webhookId: number,
  deliveryId: number,
): Promise<ApiResult<WebhookDelivery>> {
  return apiFetch<WebhookDelivery>(
    `/api/v1/webhooks/${webhookId}/deliveries/${deliveryId}/redeliver`,
    { method: "POST" },
  );
}

export type { Webhook, WebhookDelivery };
