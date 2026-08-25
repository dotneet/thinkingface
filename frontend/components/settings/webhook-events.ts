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
    value: "repo.moved",
    labelKey: "settings.webhookEvents.repoMoved.label",
    hintKey: "settings.webhookEvents.repoMoved.hint",
  },
  {
    value: "repo.transfer_requested",
    labelKey: "settings.webhookEvents.repoTransferRequested.label",
    hintKey: "settings.webhookEvents.repoTransferRequested.hint",
  },
  {
    value: "repo.archived",
    labelKey: "settings.webhookEvents.repoArchived.label",
    hintKey: "settings.webhookEvents.repoArchived.hint",
  },
  {
    value: "repo.unarchived",
    labelKey: "settings.webhookEvents.repoUnarchived.label",
    hintKey: "settings.webhookEvents.repoUnarchived.hint",
  },
  {
    value: "repo.ref_deleted",
    labelKey: "settings.webhookEvents.repoRefDeleted.label",
    hintKey: "settings.webhookEvents.repoRefDeleted.hint",
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
