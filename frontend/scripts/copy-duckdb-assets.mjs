#!/usr/bin/env node
// Copies the DuckDB-WASM runtime out of node_modules into public/duckdb/ so the
// SQL console loads it from our own origin.
//
// Why not `duckdb.getJsDelivrBundles()`, which the upstream examples use?
//   1. thinkingface is a self-hosted product: an air-gapped or firewalled
//      deployment must not need jsdelivr.com to render a page.
//   2. `new Worker(url)` refuses a cross-origin script anyway, so the CDN path
//      only works via a blob shim that hides the real failure mode.
//
// Why a copy step instead of importing the assets through the bundler? The
// .wasm files are ~35MB each and the workers are prebuilt IIFEs; handing them
// to webpack/turbopack buys nothing and costs a very slow build. public/ serves
// them verbatim, which is exactly what DuckDB wants.
//
// The asset names are hard-coded, which is why package.json pins an exact
// duckdb-wasm version: a rename upstream should fail this script loudly rather
// than ship a page that 404s on its worker.
//
// Idempotent: files already present with a matching size are left alone, so
// `bun run dev` does not re-copy ~75MB on every start. The output directory is
// gitignored — it is a build artifact, not a source file.

import { copyFileSync, mkdirSync, statSync } from "node:fs";
import { createRequire } from "node:module";
import { basename, join } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = fileURLToPath(new URL("..", import.meta.url));
const OUT_DIR = join(ROOT, "public", "duckdb");
const require = createRequire(import.meta.url);

// MVP is the baseline; EH is picked by selectBundle() on any browser with
// WebAssembly exception handling. The COI (threaded) bundle is deliberately
// left out: it needs COOP/COEP headers this app does not send.
const ASSETS = [
  "dist/duckdb-mvp.wasm",
  "dist/duckdb-browser-mvp.worker.js",
  "dist/duckdb-eh.wasm",
  "dist/duckdb-browser-eh.worker.js",
];

function resolveAsset(relative) {
  const specifier = `@duckdb/duckdb-wasm/${relative}`;
  try {
    return require.resolve(specifier);
  } catch {
    // The package's "exports" map lists every dist asset, so the line above
    // normally wins. Fall back to the conventional layout for installs that
    // hoist differently (or a resolver that declines non-JS extensions).
    const fallback = join(ROOT, "node_modules", "@duckdb", "duckdb-wasm", relative);
    statSync(fallback); // throws with a clear ENOENT if this is wrong too
    return fallback;
  }
}

mkdirSync(OUT_DIR, { recursive: true });

let copied = 0;
let skipped = 0;
for (const relative of ASSETS) {
  let source;
  try {
    source = resolveAsset(relative);
  } catch (err) {
    console.error(
      `copy-duckdb-assets: cannot find ${relative} in @duckdb/duckdb-wasm (${err.message}).\n` +
        "Run `bun install`, or update ASSETS in this script if the package renamed its files.",
    );
    process.exit(1);
  }

  const target = join(OUT_DIR, basename(relative));
  const sourceStat = statSync(source);
  let targetStat = null;
  try {
    targetStat = statSync(target);
  } catch {
    // Not copied yet.
  }
  if (targetStat && targetStat.size === sourceStat.size) {
    skipped++;
    continue;
  }
  copyFileSync(source, target);
  copied++;
}

console.log(`copy-duckdb-assets: ${copied} copied, ${skipped} already up to date -> public/duckdb`);
