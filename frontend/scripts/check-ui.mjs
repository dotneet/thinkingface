#!/usr/bin/env node
// Mechanical guard for the rules in DESIGN.md. Dependency-free on purpose: it
// runs from `bun run check:ui` and in CI without an install step beyond the
// app's own.
//
// It checks these things:
//   1. no raw `var(--tf-*)` inside a className — use the mapped Tailwind
//      utilities (`bg-bg-raised`, `text-fg-muted`, `border-border`, …) that
//      app/globals.css's `@theme inline` block generates;
//   2. no raw Tailwind palette colours (`bg-slate-700`, `text-red-500`, …) —
//      they ignore the light/dark token swap;
//   3. no bare `<button>` outside components/ui/ — use <Button>, which owns
//      the variant map and defaults `type` to "button";
//   4. no `window.confirm` / `window.alert` — use `ConfirmDialog`
//      (components/ui/confirm-dialog.tsx), which is themed, keyboard- and
//      focus-managed, and consistent across every destructive action ([S13]);
//   5. no plain value imported by a Server Component from a "use client"
//      module. React only marshals *components* across that boundary; a
//      constant or helper resolves to undefined on the server, silently, at
//      runtime only in a production build. This shipped once already:
//      file-preview.tsx read MAX_TABULAR_BYTES out of tabular-preview.tsx, so
//      `entry.size <= undefined` was always false and the CSV table preview
//      never rendered. Put shared values in a framework-free module (lib/).
//   6. `text-xs` and `text-fg-subtle` never appear in the same className
//      without a weight utility. Both are the quiet end of their scale, and
//      12px at 400 in the subtle grey is the least legible combination the
//      token set can produce — the pairing is legal, but it has to carry
//      `font-medium` (DESIGN.md §2);
//   7. a tinted fill and its own tone's *base* text token never share a
//      className. `bg-warning/20` darkens the surface by the same hue the
//      label uses, so `text-warning` on it measured 2.12:1 — that pairing
//      takes `text-warning-strong` (DESIGN.md §1). Only unprefixed
//      utilities count, so `hover:bg-negative/10` paired with a
//      `hover:text-negative-strong` is not a violation;
//   8. every static top-level segment under app/ (a directory not starting
//      with `[` or `(`) is on lib/validation.ts's RESERVED_NAMESPACE_NAMES —
//      otherwise a new route silently shadows `/[ns]` for whoever holds that
//      name as a namespace, or worse, sits unreachable behind it
//      (docs/namespace-design.md §9);
//   9. lib/validation.ts's RESERVED_NAMESPACE_NAMES and
//      backend/internal/api/names.go's reservedNamespaceNames name the exact
//      same set — the frontend list only saves a round trip, the Go one is
//      authoritative, and letting them drift means the UI accepts a name the
//      server is certain to reject (or the other way around);
//  10. no `type="search"` outside components/ui/. A native search field's
//      clear "×" empties the input and fires `change`, but never submits —
//      so a hand-rolled box reads as cleared while the URL and results stay
//      on the old term. Three separate PRs shipped that bug in three places,
//      each with a different workaround. `SearchInput` (submits on clear)
//      and `FilterInput` (filters as you type) own the semantics now
//      (components/ui/search-input.tsx, DESIGN.md §9).
//
// Scanning notes:
//   - comments are blanked out before matching, so prose that *mentions*
//     `<button>` or a palette class (as components/ui/button.tsx's own doc
//     comment does) is not a violation;
//   - `className={…}` values are read by matching braces rather than with a
//     regex, so `cn("p-2", { "bg-red-500": on })` and multi-line class
//     expressions are covered;
//   - a directory that cannot be read is a hard error, never a silent skip:
//     an unreadable subtree used to make the whole check report "no problems".

import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const SCAN_DIRS = ["app", "components", "hooks", "lib"];
const SKIP_DIRS = new Set(["node_modules", ".next", "out", ".git", "dist", "build"]);
const EXTENSIONS = [".ts", ".tsx"];

/**
 * Files allowed to render a bare <button>. Keep this list short and justify
 * every entry: anything else belongs in components/ui/.
 *
 * Paths are relative to frontend/ and use forward slashes.
 */
const RAW_BUTTON_ALLOWLIST = new Set([
  // The primitive itself, plus anything else under components/ui/, is exempt
  // (see isUiPrimitive below). Add one-off exceptions here.
]);

/**
 * Files allowed to call `window.confirm` / `window.alert`. Keep this list
 * short and justify every entry: destructive-action confirmation belongs in
 * `ConfirmDialog` (components/ui/confirm-dialog.tsx) instead — see [S13].
 *
 * Paths are relative to frontend/ and use forward slashes.
 */
const WINDOW_CONFIRM_ALLOWLIST = new Set([]);

/**
 * Files allowed to render their own `type="search"` input. Keep this list
 * empty if you can: `SearchInput` / `FilterInput`
 * (components/ui/search-input.tsx) exist precisely so no caller has to
 * remember what the browser's clear control does.
 *
 * Paths are relative to frontend/ and use forward slashes.
 */
const RAW_SEARCH_TYPE_ALLOWLIST = new Set([]);

const TAILWIND_PALETTE = [
  "slate",
  "gray",
  "zinc",
  "neutral",
  "stone",
  "red",
  "orange",
  "amber",
  "yellow",
  "lime",
  "green",
  "emerald",
  "teal",
  "cyan",
  "sky",
  "blue",
  "indigo",
  "violet",
  "purple",
  "fuchsia",
  "pink",
  "rose",
].join("|");

const RAW_PALETTE_RE = new RegExp(
  `\\b(?:bg|text|border|ring|fill|stroke)-(?:${TAILWIND_PALETTE})-[0-9]+\\b`,
  "g",
);
const CSS_VAR_RE = /var\(--tf-/;
const SUBTLE_XS_RE = /\btext-xs\b/;
// A tinted fill (`bg-positive/15`) and the opaque accent chip, plus the base
// tone text tokens that must not sit on them. The lookbehind rejects a
// variant-prefixed utility (`hover:bg-negative/10`): a tint that only applies
// in one state is paired with a text colour for that same state, which the
// className as a whole cannot tell us, so those are left to review.
const TONE_TINT_RE =
  /(?<![\w:-])bg-(accent)-muted\b|(?<![\w:-])bg-(positive|negative|warning)\/\d+/g;
const TONE_TEXT_RE = /(?<![\w:-])text-(accent|positive|negative|warning)(?![\w-])/g;
// Only the weights *above* the default count: the rule asks for extra weight,
// so `font-normal` (and anything lighter) satisfies nothing.
const WEIGHT_RE = /\bfont-(?:medium|semibold|bold|extrabold|black)\b/;
// `\b` after the tag name so `<button` at the end of a line (how Biome formats
// any button with more than one or two props) is caught, while `<ButtonGroup`
// and `<buttonish` are not.
const RAW_BUTTON_RE = /<button\b/g;
const WINDOW_CONFIRM_ALERT_RE = /\bwindow\.(confirm|alert)\s*\(/g;
// `type="search"` / `type={"search"}` on any element. Only the primitives in
// components/ui/ are allowed to render one (see rule 10).
const RAW_SEARCH_TYPE_RE = /\btype=\{?["']search["']\}?/g;

const CLASSNAME_START_RE = /\bclass(?:Name)?\s*=\s*/g;

function walk(dir, out = []) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (SKIP_DIRS.has(entry.name)) continue;
    const full = join(dir, entry.name);
    // Never follow symlinks: a link out of the tree would be scanned twice (or,
    // when dangling, abort the walk).
    if (entry.isSymbolicLink()) continue;
    if (entry.isDirectory()) walk(full, out);
    else if (entry.isFile() && EXTENSIONS.some((ext) => entry.name.endsWith(ext))) out.push(full);
  }
  return out;
}

/**
 * Blank out comments, preserving every offset and newline so reported line
 * numbers still line up with the original source. String and template literals
 * are tracked so a `"https://…"` URL is not mistaken for a line comment.
 */
function blankComments(source) {
  const out = source.split("");
  const n = source.length;
  let i = 0;
  const blank = (from, to) => {
    for (let k = from; k < to; k++) if (out[k] !== "\n") out[k] = " ";
  };

  while (i < n) {
    const c = source[i];
    // `source[i - 1] !== ":"` keeps a bare `https://…` from starting a comment
    // when the URL was not recognised as being inside a string (which happens
    // after a JSX attribute string that spans lines).
    if (c === "/" && source[i + 1] === "/" && source[i - 1] !== ":") {
      let j = i + 2;
      while (j < n && source[j] !== "\n") j++;
      blank(i, j);
      i = j;
    } else if (c === "/" && source[i + 1] === "*") {
      let j = i + 2;
      while (j < n && !(source[j] === "*" && source[j + 1] === "/")) j++;
      j = Math.min(n, j + 2);
      blank(i, j);
      i = j;
    } else if (c === '"' || c === "'" || c === "`") {
      let j = i + 1;
      while (j < n) {
        if (source[j] === "\\") {
          j += 2;
          continue;
        }
        if (source[j] === c) break;
        // An unterminated single/double-quoted string cannot span lines; bail
        // out at the newline so a broken file does not swallow the rest.
        if (c !== "`" && source[j] === "\n") break;
        j++;
      }
      i = Math.min(n, j + 1);
    } else {
      i++;
    }
  }
  return out.join("");
}

/**
 * Read a `className=` / `class=` attribute value starting at `start` (the first
 * character after the `=` and any whitespace). Returns the value's source text,
 * or null when the attribute is not a form we inspect (e.g. a spread).
 */
function readAttributeValue(source, start) {
  const c = source[start];
  if (c === '"' || c === "'") {
    const end = source.indexOf(c, start + 1);
    if (end === -1) return source.slice(start + 1); // unterminated: take the rest
    return source.slice(start + 1, end);
  }
  if (c !== "{") return null;
  // Brace matching, skipping over string/template literals so a `}` inside a
  // string does not end the expression early.
  let depth = 0;
  let i = start;
  const n = source.length;
  while (i < n) {
    const ch = source[i];
    if (ch === '"' || ch === "'" || ch === "`") {
      let j = i + 1;
      while (j < n) {
        if (source[j] === "\\") {
          j += 2;
          continue;
        }
        if (source[j] === ch) break;
        if (ch !== "`" && source[j] === "\n") break;
        j++;
      }
      i = j + 1;
      continue;
    }
    if (ch === "{") depth++;
    else if (ch === "}") {
      depth--;
      if (depth === 0) return source.slice(start + 1, i);
    }
    i++;
  }
  return source.slice(start + 1); // unbalanced: take the rest
}

function lineOf(source, index) {
  let line = 1;
  for (let i = 0; i < index; i++) if (source[i] === "\n") line++;
  return line;
}

function isUiPrimitive(relPath) {
  return relPath.startsWith(`components${sep}ui${sep}`);
}

/** True when a module opts into the client bundle with a "use client" directive. */
function isClientSource(raw) {
  // The directive has to be the first statement, but may sit under a comment
  // banner or a shebang, so skip those rather than testing the raw prefix.
  const head = blankComments(raw).trimStart();
  return head.startsWith('"use client"') || head.startsWith("'use client'");
}

/** Resolve an `@/…` specifier to a file under frontend/, or null. */
function resolveAlias(spec) {
  if (!spec.startsWith("@/")) return null;
  const base = join(ROOT, spec.slice(2));
  for (const candidate of [
    ...EXTENSIONS.map((ext) => base + ext),
    ...EXTENSIONS.map((ext) => join(base, `index${ext}`)),
  ]) {
    if (existsSync(candidate)) return candidate;
  }
  return null;
}

/**
 * A name React can legitimately hand across the boundary: a component, which
 * the bundler replaces with a client reference. `MAX_ROWS` or `parseTabular`
 * cannot be — those are the ones this rule is about. Hooks (`useThing`) are
 * excluded too, since a Server Component cannot call one at all.
 */
function isComponentName(name) {
  return /^[A-Z]/.test(name) && name !== name.toUpperCase();
}

// `import … from "…"` — the clause is captured loosely and split below.
const IMPORT_RE = /^import\s+(type\s+)?([\s\S]*?)\s+from\s+"([^"]+)";/gm;

const problems = [];

function report(relPath, line, rule, detail) {
  problems.push({ file: relPath, line, rule, detail });
}

/**
 * Pulls the quoted string entries out of `lib/validation.ts`'s
 * `RESERVED_NAMESPACE_NAMES` array literal. Reading the source with a regex
 * rather than importing the module keeps this script dependency- and
 * TypeScript-loader-free (see the file banner).
 */
function readFrontendReservedNames() {
  const relPath = join("lib", "validation.ts");
  const absPath = join(ROOT, relPath);
  let raw;
  try {
    raw = readFileSync(absPath, "utf8");
  } catch (err) {
    console.error(`check-ui: cannot read ${relPath}: ${err.message}`);
    process.exit(2);
  }
  // Anchored on the full declaration (not just the identifier) so the "["
  // that opens `readonly string[]`'s type annotation is not mistaken for the
  // one that opens the array literal a few characters later.
  const marker = "RESERVED_NAMESPACE_NAMES: readonly string[] = [";
  const markerIdx = raw.indexOf(marker);
  const start = markerIdx === -1 ? -1 : markerIdx + marker.length - 1;
  const end = start === -1 ? -1 : raw.indexOf("]", start + 1);
  if (markerIdx === -1 || end === -1) {
    console.error(`check-ui: could not find RESERVED_NAMESPACE_NAMES array in ${relPath}`);
    process.exit(2);
  }
  const body = raw.slice(start + 1, end);
  const names = [...body.matchAll(/"([^"]+)"/g)].map((m) => m[1]);
  return { names, relPath };
}

/**
 * Pulls the quoted keys out of `backend/internal/api/names.go`'s
 * `reservedNamespaceNames` map literal — the two lists must name the same
 * set (docs/namespace-design.md §9).
 */
function readBackendReservedNames() {
  const relPath = join("..", "backend", "internal", "api", "names.go");
  const absPath = join(ROOT, relPath);
  let raw;
  try {
    raw = readFileSync(absPath, "utf8");
  } catch (err) {
    console.error(`check-ui: cannot read ${relPath}: ${err.message}`);
    process.exit(2);
  }
  const marker = "reservedNamespaceNames = map[string]bool{";
  const markerIdx = raw.indexOf(marker);
  const start = markerIdx === -1 ? -1 : markerIdx + marker.length;
  const end = start === -1 ? -1 : raw.indexOf("}", start);
  if (markerIdx === -1 || end === -1) {
    console.error(`check-ui: could not find reservedNamespaceNames map in ${relPath}`);
    process.exit(2);
  }
  const body = raw.slice(start, end);
  const names = [...body.matchAll(/"([^"]+)":\s*true/g)].map((m) => m[1]);
  return { names, relPath };
}

/**
 * Rule 8 + 9 (see the file banner): every static top-level app/ route is
 * reserved, and the frontend/backend reserved-name lists name the same set.
 * Neither check is per-file, so both run once here rather than inside the
 * main scan loop.
 */
function checkReservedNamespaceNames() {
  const frontend = readFrontendReservedNames();
  const backend = readBackendReservedNames();
  const frontendSet = new Set(frontend.names);
  const backendSet = new Set(backend.names);

  for (const name of backend.names) {
    if (!frontendSet.has(name)) {
      report(
        frontend.relPath,
        1,
        "reserved-name-sync",
        `"${name}" is reserved in ${backend.relPath} but missing from RESERVED_NAMESPACE_NAMES`,
      );
    }
  }
  for (const name of frontend.names) {
    if (!backendSet.has(name)) {
      report(
        backend.relPath,
        1,
        "reserved-name-sync",
        `"${name}" is in RESERVED_NAMESPACE_NAMES but missing from reservedNamespaceNames in ${frontend.relPath}`,
      );
    }
  }

  const appDir = join(ROOT, "app");
  let entries;
  try {
    entries = readdirSync(appDir, { withFileTypes: true });
  } catch (err) {
    console.error(`check-ui: cannot scan app: ${err.message}`);
    process.exit(2);
  }
  for (const entry of entries) {
    if (!entry.isDirectory()) continue;
    // `[ns]` is the dynamic namespace route itself; `(group)` route groups
    // contribute no URL segment of their own — neither can collide with a
    // reserved name.
    if (entry.name.startsWith("[") || entry.name.startsWith("(")) continue;
    if (!frontendSet.has(entry.name.toLowerCase())) {
      report(
        join("app", entry.name),
        1,
        "reserved-name-sync",
        `app/${entry.name} is a static top-level route but "${entry.name}" is not in ` +
          `RESERVED_NAMESPACE_NAMES (${frontend.relPath}) — add it there and to ` +
          `reservedNamespaceNames in ${backend.relPath}`,
      );
    }
  }
}

let scanned = 0;

// First pass: which modules are in the client bundle. Rule (e) needs the whole
// picture before it can judge any single import.
const clientModules = new Set();
for (const dir of SCAN_DIRS) {
  const abs = join(ROOT, dir);
  let files;
  try {
    files = walk(abs);
  } catch (err) {
    if (err.code === "ENOENT" && err.path === abs) continue;
    console.error(`check-ui: cannot scan ${dir}: ${err.message}`);
    process.exit(2);
  }
  for (const file of files) {
    try {
      if (isClientSource(readFileSync(file, "utf8"))) clientModules.add(file);
    } catch (err) {
      console.error(`check-ui: cannot read ${relative(ROOT, file)}: ${err.message}`);
      process.exit(2);
    }
  }
}

for (const dir of SCAN_DIRS) {
  const abs = join(ROOT, dir);
  let files;
  try {
    files = walk(abs);
  } catch (err) {
    // A missing scan directory is fine (they are optional). Anything else —
    // a permission error, a dangling entry — must fail loudly rather than
    // silently dropping every file underneath it.
    if (err.code === "ENOENT" && err.path === abs) continue;
    console.error(`check-ui: cannot scan ${dir}: ${err.message}`);
    process.exit(2);
  }

  for (const file of files) {
    const relPath = relative(ROOT, file);
    const allowKey = relPath.split(sep).join("/");
    let raw;
    try {
      raw = readFileSync(file, "utf8");
    } catch (err) {
      console.error(`check-ui: cannot read ${relPath}: ${err.message}`);
      process.exit(2);
    }
    scanned++;
    // Comments are not code: `// prefer <Button> over <button>` is prose.
    const source = blankComments(raw);

    // (a) + (b): only look inside className values, so CSS variables handed to
    // a charting library (uPlot's stroke colours, for instance) stay legal.
    CLASSNAME_START_RE.lastIndex = 0;
    let attr = CLASSNAME_START_RE.exec(source);
    while (attr !== null) {
      const valueStart = attr.index + attr[0].length;
      const value = readAttributeValue(source, valueStart);
      if (value !== null) {
        const line = lineOf(source, attr.index);
        if (CSS_VAR_RE.test(value)) {
          report(relPath, line, "css-var", "className uses var(--tf-*) instead of a token utility");
        }
        // (f) 12px in the subtle grey needs the extra weight. Tested against
        // the whole className expression rather than each string literal in
        // it, so `cn("text-xs font-medium", "text-fg-subtle")` — the weight
        // and the colour arriving from different arguments — still passes.
        if (
          SUBTLE_XS_RE.test(value) &&
          value.includes("text-fg-subtle") &&
          !WEIGHT_RE.test(value)
        ) {
          report(
            relPath,
            line,
            "subtle-xs-weight",
            "text-xs + text-fg-subtle needs font-medium — 12px at 400 in the subtle grey is the " +
              "least legible pairing in the token set (DESIGN.md §2)",
          );
        }
        // (g) a tone's tinted fill next to that tone's base text token.
        TONE_TINT_RE.lastIndex = 0;
        const tinted = new Set(
          [...value.matchAll(TONE_TINT_RE)].map((m) => m[1] ?? m[2]).filter(Boolean),
        );
        if (tinted.size > 0) {
          TONE_TEXT_RE.lastIndex = 0;
          for (const [, tone] of value.matchAll(TONE_TEXT_RE)) {
            if (!tinted.has(tone)) continue;
            report(
              relPath,
              line,
              "tinted-fill-tone",
              `text-${tone} sits on a tinted fill of its own hue — use text-${tone}-strong ` +
                "(DESIGN.md §1)",
            );
          }
        }
        RAW_PALETTE_RE.lastIndex = 0;
        const seen = new Set();
        for (const [hit] of value.matchAll(RAW_PALETTE_RE)) {
          if (seen.has(hit)) continue;
          seen.add(hit);
          report(relPath, line, "raw-palette", `className uses the raw palette colour "${hit}"`);
        }
        // Continue after the attribute value so a nested `className=` inside
        // the expression is still found, but the same one is not re-matched.
        CLASSNAME_START_RE.lastIndex = valueStart + 1;
      }
      attr = CLASSNAME_START_RE.exec(source);
    }

    // (c) bare <button> outside the primitives directory.
    if (!isUiPrimitive(relPath) && !RAW_BUTTON_ALLOWLIST.has(allowKey)) {
      RAW_BUTTON_RE.lastIndex = 0;
      for (const hit of source.matchAll(RAW_BUTTON_RE)) {
        report(
          relPath,
          lineOf(source, hit.index),
          "raw-button",
          "use <Button> from components/ui/button",
        );
      }
    }

    // (e) values crossing the server/client boundary. Only Server Components
    // can get this wrong: a client module importing from another client
    // module is ordinary bundling.
    if (!isClientSource(raw)) {
      IMPORT_RE.lastIndex = 0;
      for (const hit of source.matchAll(IMPORT_RE)) {
        const [, typeOnly, clause, spec] = hit;
        if (typeOnly) continue;
        const target = resolveAlias(spec);
        if (target === null || !clientModules.has(target)) continue;
        const braced = clause.match(/\{([\s\S]*)\}/);
        if (braced === null) continue; // default / namespace import: a component
        for (const entry of braced[1].split(",")) {
          const name = entry.trim();
          if (name === "" || name.startsWith("type ")) continue;
          const local = name
            .split(/\s+as\s+/)
            .pop()
            .trim();
          if (isComponentName(local)) continue;
          report(
            relPath,
            lineOf(source, hit.index),
            "client-boundary",
            `Server Component imports the value "${local}" from the "use client" module ${spec} — ` +
              "it resolves to undefined at runtime; move it to a framework-free module",
          );
        }
      }
    }

    // (f) a hand-rolled `type="search"` box outside the primitives.
    if (!isUiPrimitive(relPath) && !RAW_SEARCH_TYPE_ALLOWLIST.has(allowKey)) {
      RAW_SEARCH_TYPE_RE.lastIndex = 0;
      for (const hit of source.matchAll(RAW_SEARCH_TYPE_RE)) {
        report(
          relPath,
          lineOf(source, hit.index),
          "raw-search-input",
          "use <SearchInput> (submits on clear) or <FilterInput> (filters as you type) from " +
            'components/ui/search-input — a bare type="search" field\'s × never submits',
        );
      }
    }

    // (d) window.confirm / window.alert anywhere — use ConfirmDialog instead.
    if (!WINDOW_CONFIRM_ALLOWLIST.has(allowKey)) {
      WINDOW_CONFIRM_ALERT_RE.lastIndex = 0;
      for (const hit of source.matchAll(WINDOW_CONFIRM_ALERT_RE)) {
        report(
          relPath,
          lineOf(source, hit.index),
          "no-window-confirm",
          `use <ConfirmDialog> from components/ui/confirm-dialog instead of window.${hit[1]}()`,
        );
      }
    }
  }
}

checkReservedNamespaceNames();

if (problems.length > 0) {
  const byRule = new Map();
  for (const p of problems) byRule.set(p.rule, (byRule.get(p.rule) ?? 0) + 1);

  console.error(`check-ui: ${problems.length} problem(s)\n`);
  for (const p of problems) {
    console.error(`  ${p.file}:${p.line}  [${p.rule}] ${p.detail}`);
  }
  console.error("");
  for (const [rule, count] of byRule) console.error(`  ${rule}: ${count}`);
  console.error("\nSee frontend/DESIGN.md for the rules behind these checks.");
  process.exit(1);
}

if (scanned === 0) {
  console.error("check-ui: scanned 0 files — is this running from frontend/?");
  process.exit(2);
}

console.log(`check-ui: no problems found (${scanned} files)`);
