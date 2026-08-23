import { Badge, type BadgeTone } from "@/components/ui/badge";
import type { MessageKey } from "@/lib/i18n";
import type { OrgRole } from "@/types/api";

/**
 * Role → look and label, in one map so the three roles read the same way on
 * every screen (docs/dev/organization-design.md §8.3): admin is the one that
 * carries power, write is ordinary, read is the quiet default.
 */
const ROLE_TONES: Record<OrgRole, BadgeTone> = {
  admin: "accent",
  write: "neutral",
  read: "muted",
};

const ROLE_LABEL_KEYS: Record<OrgRole, MessageKey> = {
  admin: "org.roles.admin",
  write: "org.roles.write",
  read: "org.roles.read",
};

export function orgRoleTone(role: OrgRole): BadgeTone {
  return ROLE_TONES[role];
}

export function orgRoleLabelKey(role: OrgRole): MessageKey {
  return ROLE_LABEL_KEYS[role];
}

/**
 * Presentational only — the label is resolved by the caller, which knows
 * whether it is a Server (`getT()`) or a Client (`useT()`) Component.
 */
export function OrgRoleBadge({ role, label }: { role: OrgRole; label: string }) {
  return <Badge tone={orgRoleTone(role)}>{label}</Badge>;
}
