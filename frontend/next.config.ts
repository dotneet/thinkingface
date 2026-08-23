import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  reactStrictMode: true,
  // Backend is not reachable at build time; no route in this app fetches
  // data during static generation (everything is force-dynamic or fetched
  // client-side), so the build never depends on API connectivity.
  eslint: {
    ignoreDuringBuilds: false,
  },
  // DuckDB-WASM is browser-only (lib/duckdb.ts imports it dynamically from a
  // client effect), but the server build still resolves the specifier — and
  // the "node" export condition points at duckdb-node.cjs, whose dynamic
  // requires webpack cannot statically analyse. Marking it external keeps that
  // module out of the server bundle and off the build log.
  serverExternalPackages: ["@duckdb/duckdb-wasm"],
};

export default nextConfig;
