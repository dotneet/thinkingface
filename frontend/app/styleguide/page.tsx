import { Boxes, Database, FileQuestion } from "lucide-react";
import { notFound } from "next/navigation";
import { StyleguideDialogDemo } from "@/app/styleguide/dialog-demo";
import { StyleguideSegmentedDemo } from "@/app/styleguide/segmented-demo";
import { Alert, type AlertTone } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button, type ButtonSize, type ButtonVariant } from "@/components/ui/button";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { CodeBlock } from "@/components/ui/code-block";
import { CopyButton } from "@/components/ui/copy-button";
import { DataTable, type DataTableColumn } from "@/components/ui/data-table";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Checkbox, Field, Input, Select, Textarea } from "@/components/ui/field";
import { FilterChip } from "@/components/ui/filter-chip";
import { JsonTree } from "@/components/ui/json-tree";
import { Markdown } from "@/components/ui/markdown";
import { Skeleton, SkeletonLines } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";

export const metadata = { title: "Styleguide — 🤔 Thinking Face" };

const BUTTON_VARIANTS: ButtonVariant[] = ["primary", "secondary", "ghost", "danger"];
const BUTTON_SIZES: ButtonSize[] = ["sm", "md"];
const ALERT_TONES: AlertTone[] = ["info", "positive", "negative", "warning"];
const BADGE_TONES = ["neutral", "muted", "accent", "positive", "negative", "warning"] as const;

const DEMO_COLUMNS = [
  { key: "id", hint: "Int64" },
  { key: "label", hint: "Utf8" },
  { key: "note", hint: "Utf8" },
];

const DEMO_ROWS = Array.from({ length: 30 }, (_, i) => ({
  id: i,
  label: `row ${i}`,
  note:
    i % 5 === 0
      ? "A deliberately long value so the cell truncates and opens the detail dialog when clicked."
      : i % 7 === 0
        ? null
        : `note ${i}`,
}));

// A 64×64 SVG avatar, base64-encoded the way `datasets` inlines an Image
// feature. The MIME is sniffed from the payload, so no extension is needed.
const DEMO_IMAGE_B64 =
  "PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSI2NCIgaGVpZ2h0PSI2NCIgdmlld0JveD0iMCAwIDY0IDY0Ij48cmVjdCB3aWR0aD0iNjQiIGhlaWdodD0iNjQiIHJ4PSI4IiBmaWxsPSIjMmI3ZmI4Ii8+PGNpcmNsZSBjeD0iMzIiIGN5PSIyNiIgcj0iMTIiIGZpbGw9IiNmMmY2ZmEiLz48cGF0aCBkPSJNOCA2MGM2LTE0IDE2LTIwIDI0LTIwczE4IDYgMjQgMjB6IiBmaWxsPSIjZjJmNmZhIi8+PC9zdmc+";

const CELL_COLUMNS: DataTableColumn[] = [
  { key: "image", hint: "struct<bytes, path>", feature: "image" },
  { key: "annotations", hint: "list<struct>" },
  { key: "meta", hint: "Utf8 / JSON", feature: "json" },
  { key: "caption", hint: "Utf8" },
];

const CELL_ROWS = Array.from({ length: 6 }, (_, i) => ({
  image:
    i === 3
      ? { bytes: null, path: "train/broken.png" }
      : { bytes: DEMO_IMAGE_B64, path: `train/${i}.svg` },
  annotations: [
    { label: "face", box: [12, 8, 40, 44], score: 0.98 },
    { label: "background", box: [0, 0, 64, 64], score: 0.51 },
  ],
  meta: `{"split":"train","index":${i},"source":{"dataset":"demo","license":"cc-by-4.0"}}`,
  caption:
    i % 2 === 0
      ? `example ${i}`
      : `A longer caption for row ${i} that runs past the eighty character cut-off used by the cell.`,
}));

const DEMO_JSON = {
  id: "run-4f2a",
  finished: true,
  score: null,
  config: {
    model: "resnet-50",
    optimizer: { name: "adamw", lr: 0.0003, betas: [0.9, 0.999] },
    tags: ["vision", "baseline"],
  },
  note: "A string longer than the tree's 200-character clip so the reveal control shows up: ".repeat(
    4,
  ),
};

const SURFACE_TOKENS = [
  { name: "bg", className: "bg-bg", use: "page canvas" },
  { name: "bg-raised", className: "bg-bg-raised", use: "cards, header, menus" },
  { name: "bg-sunken", className: "bg-bg-sunken", use: "inputs, table headers, wells" },
  { name: "bg-hover", className: "bg-bg-hover", use: "hover feedback only" },
  { name: "border", className: "bg-border", use: "default hairline" },
  { name: "border-strong", className: "bg-border-strong", use: "emphasis, blockquote rule" },
  { name: "border-control", className: "bg-border-control", use: "input / select / checkbox edge" },
];

const TEXT_TOKENS = [
  { name: "fg", className: "text-fg", use: "primary text, headings" },
  { name: "fg-muted", className: "text-fg-muted", use: "body copy, labels" },
  { name: "fg-subtle", className: "text-fg-subtle", use: "metadata, placeholders" },
];

const ACCENT_TOKENS = [
  { name: "accent", className: "bg-accent", use: "primary action, active state" },
  { name: "accent-fg", className: "bg-accent-fg", use: "text on accent" },
  { name: "accent-muted", className: "bg-accent-muted", use: "accent chips, active nav" },
  { name: "positive", className: "bg-positive", use: "success" },
  { name: "negative", className: "bg-negative", use: "errors, destructive" },
  { name: "warning", className: "bg-warning", use: "degraded / in-progress" },
  { name: "accent-strong", className: "bg-accent-strong", use: "text on accent-muted" },
  { name: "positive-strong", className: "bg-positive-strong", use: "text on bg-positive/15" },
  { name: "negative-strong", className: "bg-negative-strong", use: "text on bg-negative/15" },
  { name: "warning-strong", className: "bg-warning-strong", use: "text on bg-warning/20" },
];

const MARKDOWN_SAMPLE = `# Heading 1

## Heading 2

### Heading 3

#### Heading 4

##### Heading 5

###### Heading 6

A paragraph with a [link to the homepage](https://example.com/very/long/path/that/should/wrap/or/scroll/instead/of/blowing/out/the/layout), some \`inline code\`, **bold**, *italic*, and ~~strikethrough~~ text. Press <kbd>Ctrl</kbd>+<kbd>K</kbd> to open the command palette.

- Unordered item one
- Unordered item two
  - Nested item
    - Deeply nested item
- Unordered item three

1. Ordered item one
2. Ordered item two
   1. Nested ordered item
3. Ordered item three

- [x] Completed task
- [ ] Open task
  - [x] Nested completed task
  - [ ] Nested open task

| Column | A narrow column | A deliberately wide column that forces the table to scroll horizontally inside its own container |
| --- | --- | --- |
| Row 1 | short | Some fairly long cell content that pushes the table past the width of its wrapper |
| Row 2 | short | Another long value, this time with \`inline code\` inside the cell |
| Row 3 | short | Plain text |

\`\`\`python
def greet(name: str) -> str:
    # Say hello
    return f"Hello, {name}!"

print(greet("world"))
\`\`\`

\`\`\`json
{
  "name": "thinkingface",
  "version": "1.0.0",
  "private": true
}
\`\`\`

\`\`\`bash
docker compose up -d
curl -sS http://localhost:8080/api/whoami
\`\`\`

> A blockquote spanning a single line.
>
> A blockquote spanning multiple lines, to check line-height and the left rule.

---

![A descriptive alt text for a working image](https://placehold.co/480x160?text=sample+image)

![Alt text for a broken image URL](https://example.invalid/this-image-does-not-exist.png)

<div align="center">

Centered content inside a raw \`<div align="center">\`.

</div>

Mass–energy equivalence: $E=mc^2$

$$
\\int_{-\\infty}^{\\infty} e^{-x^2}\\,dx = \\sqrt{\\pi}
$$

A statement with a footnote reference.[^1]

<details>
<summary>Click to expand</summary>

Hidden content revealed via native \`<details>\`/\`<summary>\`.

</details>

[^1]: This is the footnote definition, rendered at the end of the document.
`;

const TYPE_SCALE = [
  { className: "text-3xl font-semibold tracking-tight", label: "text-3xl semibold — page hero" },
  { className: "text-2xl font-semibold tracking-tight", label: "text-2xl semibold — page title" },
  { className: "text-sm font-medium", label: "text-sm medium — labels, table cells" },
  { className: "text-sm", label: "text-sm — body copy, table cells" },
  {
    className: "text-xs font-medium text-fg-subtle",
    label: "text-xs medium subtle — metadata (subtle always pairs with medium)",
  },
  { className: "font-mono text-xs", label: "font-mono text-xs — paths, tensors, tokens" },
];

/**
 * Living inventory of every ui/ primitive. Development-only: it exists to be
 * read while building screens, not to ship as a public route.
 */
export default function StyleguidePage() {
  if (process.env.NODE_ENV === "production") notFound();

  return (
    <div className="flex flex-col gap-10 py-6">
      <header className="flex flex-col gap-1">
        <h1 className="text-3xl font-semibold tracking-tight">Styleguide</h1>
        <p className="max-w-2xl text-sm text-fg-subtle">
          Every primitive in <code className="font-mono text-xs">components/ui/</code> with all of
          its variants, plus the semantic colour tokens and the type scale. Development-only route.
        </p>
      </header>

      <Section title="Semantic colours" hint="Surfaces">
        <SwatchGrid items={SURFACE_TOKENS} />
      </Section>

      <Section title="Semantic colours" hint="Foreground">
        <div className="flex flex-col gap-2">
          {TEXT_TOKENS.map((t) => (
            <div key={t.name} className="flex items-baseline gap-3">
              <span className={`text-sm font-medium ${t.className}`}>
                The quick brown fox — {t.name}
              </span>
              <span className="text-xs font-medium text-fg-subtle">{t.use}</span>
            </div>
          ))}
        </div>
      </Section>

      <Section title="Semantic colours" hint="Accent and status">
        <SwatchGrid items={ACCENT_TOKENS} />
      </Section>

      <Section title="Typography">
        <div className="flex flex-col gap-3">
          {TYPE_SCALE.map((t) => (
            <p key={t.label} className={t.className}>
              {t.label}
            </p>
          ))}
        </div>
      </Section>

      <Section title="Button" hint="variant × size">
        <div className="flex flex-col gap-3">
          {BUTTON_SIZES.map((size) => (
            <div key={size} className="flex flex-wrap items-center gap-2">
              <span className="w-8 font-mono text-xs font-medium text-fg-subtle">{size}</span>
              {BUTTON_VARIANTS.map((variant) => (
                <Button key={variant} variant={variant} size={size}>
                  {variant}
                </Button>
              ))}
              <Button variant="primary" size={size} disabled>
                disabled
              </Button>
            </div>
          ))}
        </div>
      </Section>

      <Section title="Badge" hint="tone">
        <div className="flex flex-wrap items-center gap-2">
          {BADGE_TONES.map((tone) => (
            <Badge key={tone} tone={tone}>
              {tone}
            </Badge>
          ))}
        </div>
      </Section>

      <Section title="FilterChip" hint="one active filter, with its remove link">
        <div className="flex flex-wrap items-center gap-2">
          <FilterChip
            label="Tag"
            value="text-generation"
            href="/styleguide"
            removeLabel="Remove filter: text-generation"
          />
          <FilterChip
            label="Derived from"
            value="meta-llama/Llama-3-8B"
            href="/styleguide"
            removeLabel="Remove filter: meta-llama/Llama-3-8B"
          />
          <FilterChip
            value="Base models only"
            href="/styleguide"
            removeLabel="Remove filter: Base models only"
          />
        </div>
      </Section>

      <Section title="Alert" hint="tone">
        <div className="flex flex-col gap-2">
          {ALERT_TONES.map((tone) => (
            <Alert key={tone} tone={tone} title={`${tone} alert`}>
              A short explanation of what happened and what to do about it.
            </Alert>
          ))}
        </div>
      </Section>

      <Section title="Form controls">
        <div className="flex max-w-md flex-col gap-3">
          <Field label="Input" hint="Single-line text">
            <Input placeholder="e.g. laptop-cli" />
          </Field>
          <Field label="Select">
            <Select defaultValue="read">
              <option value="read">Read</option>
              <option value="write">Write</option>
            </Select>
          </Field>
          <Field label="Textarea">
            <Textarea className="min-h-20" placeholder="What is this repository for?" />
          </Field>
          <label className="flex items-center gap-2 text-sm text-fg-muted">
            <Checkbox defaultChecked />
            Checkbox
          </label>
          <Field label="Disabled">
            <Input disabled value="Not editable" readOnly />
          </Field>
        </div>
      </Section>

      <Section title="Card">
        <Card className="max-w-md">
          <CardHeader>
            <CardTitle>Card title</CardTitle>
            <Badge tone="accent">accent</Badge>
          </CardHeader>
          <p className="mt-2 text-sm text-fg-subtle">
            The raised surface used for every boxed block on a page.
          </p>
        </Card>
      </Section>

      <Section title="Loading states" hint="Spinner and Skeleton">
        <div className="flex flex-col gap-4">
          <div className="flex items-center gap-3 text-sm text-fg-subtle">
            <Spinner size={14} />
            <Spinner />
            <Spinner size={24} />
            <span>inline refresh</span>
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <Skeleton className="h-5 w-24" />
            <Skeleton className="h-8 w-8 rounded-full" />
            <Skeleton className="h-16 w-40 rounded-lg" />
          </div>
          <SkeletonLines lines={3} className="max-w-md" />
        </div>
      </Section>

      <Section title="Empty and error states">
        <div className="flex flex-col gap-4">
          <EmptyState
            icon={Database}
            title="No datasets yet"
            description="Push one with the CLI, or create it here."
            action={<Button variant="primary">New dataset</Button>}
          />
          <ErrorState
            title="Couldn't load this"
            message="The backend API is unreachable."
            hint="Check that the API server is running on port 8080."
            action={<Button>Retry</Button>}
          />
          <EmptyState icon={FileQuestion} title="Minimal empty state" />
        </div>
      </Section>

      <Section title="Dialog" hint="native <dialog>">
        <StyleguideDialogDemo />
      </Section>

      <Section title="Copy button">
        <div className="flex items-center gap-3">
          <CopyButton value="tf_example_token" />
          <CopyButton value="git clone …" label="Copy command" />
          {/* `value` also accepts a thunk for expensive strings — not shown
              here because a Server Component cannot hand a function to a
              Client Component. See SqlConsole's "Copy CSV". */}
        </div>
      </Section>

      <Section title="Code block" hint="labelled and unlabelled">
        <div className="flex max-w-md flex-col gap-4">
          <CodeBlock value="git clone https://example.com/org/repo.git" label="git clone" />
          <CodeBlock value="tf_example_token_do_not_use" />
          <CodeBlock
            value={Array.from({ length: 8 }, (_, i) => `line ${i}: gsutil cp foo bar`).join("\n")}
            label="Scrollable script"
            maxHeight="max-h-24"
          />
        </div>
      </Section>

      <Section title="Segmented control" hint="in-page mode switch">
        <StyleguideSegmentedDemo />
      </Section>

      <Section title="Data table" hint="virtualized, click a long cell to expand">
        <DataTable columns={DEMO_COLUMNS} rows={DEMO_ROWS} className="max-h-64" />
      </Section>

      <Section title="Markdown" hint=".tf-markdown — README / model card rendering">
        <Card className="max-w-3xl p-6">
          <Markdown source={MARKDOWN_SAMPLE} />
        </Card>
      </Section>

      <Section
        title="Cell renderers"
        hint="image thumbnails and JSON trees — click any cell to expand"
      >
        <div className="flex flex-col gap-3">
          <DataTable columns={CELL_COLUMNS} rows={CELL_ROWS} className="max-h-64" />
          <p className="text-xs font-medium text-fg-subtle">
            An <code className="font-mono">image</code> column renders the HF{" "}
            <code className="font-mono">{"{bytes, path}"}</code> struct as a thumbnail; nested and{" "}
            <code className="font-mono">JSON</code> columns open a collapsible tree with a Raw
            toggle. The MIME type is sniffed from the payload — see lib/cell-value.ts.
          </p>
        </div>
      </Section>

      <Section title="JSON tree" hint="standalone primitive, two levels open by default">
        <div className="max-w-xl rounded-lg border border-border bg-bg-raised p-3">
          <JsonTree value={DEMO_JSON} />
        </div>
      </Section>

      <Section title="Icons" hint="lucide-react">
        <div className="flex flex-wrap items-center gap-6 text-fg-muted">
          <span className="flex items-center gap-1.5 text-sm">
            <Boxes size={16} />
            size 16 — inline with body text
          </span>
          <span className="flex items-center gap-1.5 text-sm">
            <Boxes size={28} strokeWidth={1.5} />
            size 28, strokeWidth 1.5 — empty/error states
          </span>
        </div>
      </Section>
    </div>
  );
}

function Section({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-baseline gap-2 border-b border-border pb-2">
        <h2 className="text-2xl font-semibold tracking-tight">{title}</h2>
        {hint && <span className="font-mono text-xs font-medium text-fg-subtle">{hint}</span>}
      </div>
      {children}
    </section>
  );
}

function SwatchGrid({ items }: { items: { name: string; className: string; use: string }[] }) {
  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-3">
      {items.map((item) => (
        <div
          key={item.name}
          className="flex items-center gap-3 rounded-lg border border-border p-2"
        >
          <span
            className={`h-10 w-10 shrink-0 rounded-md border border-border ${item.className}`}
          />
          <span className="flex min-w-0 flex-col">
            <span className="truncate font-mono text-xs text-fg">{item.name}</span>
            <span className="truncate text-xs font-medium text-fg-subtle">{item.use}</span>
          </span>
        </div>
      ))}
    </div>
  );
}
