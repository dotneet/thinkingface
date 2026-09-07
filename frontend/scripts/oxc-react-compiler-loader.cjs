/**
 * Turbopack/webpack loader that runs the React Compiler through oxc.
 *
 * ## Why a loader and not `reactCompiler: true`
 *
 * Next.js has a first-class `reactCompiler` option, but it drives
 * `babel-plugin-react-compiler`: every matching module leaves the Rust
 * pipeline, gets parsed by Babel, transformed, printed, and handed back.
 * The docs say it plainly — "expect compile times in development and during
 * builds to be higher when enabling this option as the React Compiler relies
 * on Babel".
 *
 * `oxc-transform-react` is the same React Compiler (vendored by the oxc
 * project, not a reimplementation) running on oxc's own AST in Rust. It gets
 * through all 395 source files in this app in ~50ms. Measured end to end, five
 * interleaved cold builds each: no compiler 8.94s, this loader 8.99s,
 * `reactCompiler: true` 9.83s.
 *
 * Wiring it in as a loader is the only integration point Next.js offers today:
 * there is no `reactCompiler: { transform: 'oxc' }` switch, and
 * `@vitejs/plugin-react` v6's `compiler` option is Vite-only.
 *
 * ## What it does to a module
 *
 * `transform()` runs the React Compiler on the pristine AST first, then strips
 * TypeScript syntax. `jsx: "preserve"` keeps the JSX exactly as written, so
 * Turbopack still sees a normal JSX module afterwards and applies its own JSX
 * transform, RSC boundary analysis, and Fast Refresh wiring. Directives
 * (`"use client"`, `"use server"`) survive the round trip, which is load
 * bearing: lose the directive and every client component in the app silently
 * becomes a Server Component. Nothing about that failure looks like a build
 * error, so `oxc-react-compiler-loader.test.ts` asserts it directly.
 *
 * ## Failure policy
 *
 * The React Compiler bails out of individual functions all the time and says
 * so through `Warning`-severity diagnostics: a `react-hooks/exhaustive-deps`
 * suppression in the file, `useReactTable()`/`useVirtualizer()` returning
 * unmemoizable functions, syntax the compiler has not implemented yet. Of the
 * 232 modules the loader sees in a production build, 135 come back memoized
 * and twelve trip one of those diagnostics. None of them are errors — the
 * function is simply left unmemoized — so they are ignored rather than logged
 * on every build. `react/incompatible-library` in .oxlintrc.json surfaces the
 * library cases in the linter instead, where they can be read once.
 *
 * `fatal` is different: the compiler emitted nothing and `code` is empty. In
 * that case the original source is passed through untouched, with a warning.
 * Memoization is an optimization, so degrading to the unoptimized module is
 * strictly better than failing a build; if the source is genuinely unparsable,
 * Turbopack reports it a moment later anyway.
 */

/** @type {Promise<typeof import("oxc-transform-react")> | undefined} */
let modulePromise;

/**
 * `oxc-transform-react` is ESM-only and this loader has to be CommonJS (the
 * loader runner Turbopack embeds resolves loader paths through `require`), so
 * the import is deferred and memoised rather than done at module scope.
 */
function loadOxc() {
  modulePromise ??= import("oxc-transform-react");
  return modulePromise;
}

/**
 * @this {import("webpack").LoaderContext<unknown>}
 * @param {string} source
 */
module.exports = function oxcReactCompilerLoader(source) {
  const callback = this.async();
  const emitWarning = this.emitWarning.bind(this);
  const resourcePath = this.resourcePath;

  void (async () => {
    try {
      const { text, map } = await compile(resourcePath, source, emitWarning);
      callback(null, text, map);
    } catch (error) {
      callback(/** @type {Error} */ (error));
    }
  })();
};

/**
 * @param {string} resourcePath
 * @param {string} source
 * @param {(warning: Error) => void} emitWarning
 */
async function compile(resourcePath, source, emitWarning) {
  const { transform } = await loadOxc();
  const result = await transform(resourcePath, source, {
    lang: resourcePath.endsWith("x") ? "tsx" : "ts",
    sourceType: "module",
    sourcemap: true,
    // Leave JSX for Turbopack; the compiler only needs to see it.
    jsx: "preserve",
    reactCompiler: { target: "19" },
  });

  if (result.fatal || !result.code) {
    const first = result.errors.find((e) => e.severity === "Error");
    emitWarning(
      new Error(
        `oxc React Compiler bailed out of ${resourcePath}; the module is bundled unoptimized.` +
          (first ? ` (${first.message})` : ""),
      ),
    );
    return { text: source, map: undefined };
  }

  return { text: result.code, map: result.map };
}
