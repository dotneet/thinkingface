import type { LucideIcon } from "lucide-react";
import { Boxes, Database, FlaskConical } from "lucide-react";
import type { MessageKey } from "@/lib/i18n";

export type NavItem = { href: string; labelKey: MessageKey; icon: LucideIcon };

// Shared by the desktop nav (a Server Component) and MobileNav (a Client
// Component). Both import it directly: an icon is a function, and handing one
// across the server/client boundary as a prop fails to serialise. Labels are
// dictionary keys so each side can translate with its own translator.
export const navItems: NavItem[] = [
  { href: "/datasets", labelKey: "nav.datasets", icon: Database },
  { href: "/models", labelKey: "nav.models", icon: Boxes },
  { href: "/experiments", labelKey: "nav.experiments", icon: FlaskConical },
];
