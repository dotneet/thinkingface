import { FlatCompat } from "@eslint/eslintrc";

const compat = new FlatCompat({
  baseDirectory: import.meta.dirname,
});

const eslintConfig = [
  ...compat.extends("next/core-web-vitals", "next/typescript"),
  {
    // public/ is served verbatim and holds vendored build artifacts — the
    // DuckDB-WASM workers staged by scripts/copy-duckdb-assets.mjs are ~800KB
    // of minified emscripten output each, and linting them is both pointless
    // and very slow.
    ignores: [".next/**", "node_modules/**", "public/**", "next-env.d.ts"],
  },
];

export default eslintConfig;
