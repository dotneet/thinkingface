/**
 * Section wrapper: a heading, an optional blurb, and the content below it.
 *
 * The run detail page uses it for the sections that sit on their own — the
 * charts and the "Outputs & environment" card — so it lives somewhere every
 * section component can reach.
 */
export function Section({
  title,
  description,
  action,
  children,
}: {
  title: string;
  description?: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-col gap-0.5">
          <h2 className="text-sm font-semibold">{title}</h2>
          {description && <p className="text-xs font-medium text-fg-subtle">{description}</p>}
        </div>
        {action}
      </div>
      {children}
    </section>
  );
}
