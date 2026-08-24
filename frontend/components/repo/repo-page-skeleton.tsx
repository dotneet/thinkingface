import { Skeleton } from "@/components/ui/skeleton";

/**
 * First paint for every repository screen (DESIGN.md §4).
 *
 * The chrome above the content is identical on the overview, the file tree, a
 * blob, the commit list, the viewer, the editor and settings —
 * `RepoBreadcrumb`, the `text-2xl` title, then `RepoTabs` — so the placeholder
 * for it lives here once rather than being redrawn in a dozen `loading.tsx`
 * files. Each route passes the shape of its own content as `children`.
 */
export function RepoPageSkeleton({ children }: { children?: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-4">
      {/* RepoBreadcrumb: models / ns / name */}
      <Skeleton className="h-5 w-64" />

      {/* The repository's full name, text-2xl */}
      <Skeleton className="h-8 w-72" />

      {/* RepoTabs, which sits on the border that separates it from the body */}
      <div className="flex gap-1 border-b border-border pb-2">
        {["card", "files", "viewer", "settings"].map((tab) => (
          <Skeleton key={tab} className="h-6 w-20" />
        ))}
      </div>

      {children}
    </div>
  );
}

/**
 * The `FileNav` row (path breadcrumb on the left, revision switcher on the
 * right) that the tree, blob, commits and edit screens all render directly
 * under the tabs.
 */
export function FileNavSkeleton() {
  return (
    <div className="flex flex-wrap items-center justify-between gap-2">
      <Skeleton className="h-8 w-56" />
      <Skeleton className="h-8 w-32" />
    </div>
  );
}

/** A boxed table placeholder: header strip plus `rows` evenly spaced lines. */
export function TableSkeleton({ rows = 8 }: { rows?: number }) {
  return (
    <div className="overflow-hidden rounded-lg border border-border">
      <Skeleton className="h-9 w-full rounded-none" />
      <div className="flex flex-col gap-3 p-3">
        {Array.from({ length: rows }, (_, i) => `row-${i}`).map((key) => (
          <Skeleton key={key} className="h-4 w-full" />
        ))}
      </div>
    </div>
  );
}
