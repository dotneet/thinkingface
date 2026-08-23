import type { MessageKey } from "@/lib/i18n";
import type { WebhookEvent } from "@/types/api";

export const WEBHOOK_EVENT_OPTIONS: {
  value: WebhookEvent;
  labelKey: MessageKey;
  hintKey: MessageKey;
}[] = [
  {
    value: "repo.push",
    labelKey: "settings.webhookEvents.repoPush.label",
    hintKey: "settings.webhookEvents.repoPush.hint",
  },
  {
    value: "repo.created",
    labelKey: "settings.webhookEvents.repoCreated.label",
    hintKey: "settings.webhookEvents.repoCreated.hint",
  },
  {
    value: "repo.deleted",
    labelKey: "settings.webhookEvents.repoDeleted.label",
    hintKey: "settings.webhookEvents.repoDeleted.hint",
  },
  {
    value: "run.finished",
    labelKey: "settings.webhookEvents.runFinished.label",
    hintKey: "settings.webhookEvents.runFinished.hint",
  },
  {
    value: "run.failed",
    labelKey: "settings.webhookEvents.runFailed.label",
    hintKey: "settings.webhookEvents.runFailed.hint",
  },
];
