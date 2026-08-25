/**
 * Section wrapper: a heading, an optional blurb, and the content below it.
 *
 * The run detail page is a stack of these — summary, charts, artifacts,
 * checkpoints, note, hyperparameters, TrainingArguments, environment, danger
 * zone — and each section is its own component, so the wrapper has to live
 * somewhere all of them can reach.
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
