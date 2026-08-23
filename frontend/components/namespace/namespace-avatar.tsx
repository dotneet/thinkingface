import { cn } from "@/lib/cn";
import type { NamespaceKind } from "@/types/api";

/** Splits on the separators namespaces actually use ("-", "_", ".") and takes
 * the first letter of up to two segments, e.g. "acme-labs" -> "AL". */
function initialsOf(name: string): string {
  const parts = name.split(/[-_.\s]+/).filter(Boolean);
  const [first, second] = parts;
  if (!first) return "?";
  if (!second) return first.slice(0, 2).toUpperCase();
  return `${first[0]}${second[0]}`.toUpperCase();
}

/**
 * A namespace's avatar — one component for users and organisations alike
 * (docs/namespace-design.md §8.1).
 *
 * `avatar_url` links to an image hosted elsewhere (uploads are out of scope),
 * so a plain <img> is right here: next/image would want the remote host
 * allow-listed in next.config.ts, which a self-hosted deployment cannot know
 * in advance. Without a URL we draw the initials rather than a broken image.
 * The two kinds differ only in shape — a circle for a person, a rounded
 * square for an organisation — so the kind reads at a glance in a list.
 */
export function NamespaceAvatar({
  name,
  avatarUrl,
  kind = "user",
  size = 40,
  className,
}: {
  name: string;
  avatarUrl?: string;
  kind?: NamespaceKind;
  /** Rendered box in pixels; an image is cropped to a square. */
  size?: number;
  className?: string;
}) {
  const box = { width: size, height: size };
  const shape = kind === "org" ? "rounded-lg" : "rounded-full";

  if (avatarUrl) {
    return (
      // The URL is user-supplied and points at an arbitrary host, which
      // next/image cannot optimise without a static allow-list.
      // eslint-disable-next-line @next/next/no-img-element
      <img
        src={avatarUrl}
        alt=""
        referrerPolicy="no-referrer"
        style={box}
        className={cn("shrink-0 border border-border object-cover", shape, className)}
      />
    );
  }

  return (
    <span
      aria-hidden="true"
      style={{ ...box, fontSize: Math.round(size * 0.36) }}
      className={cn(
        "flex shrink-0 select-none items-center justify-center bg-accent-muted font-semibold text-accent-strong",
        shape,
        className,
      )}
    >
      {initialsOf(name)}
    </span>
  );
}
