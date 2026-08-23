import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

// Node environment on purpose: only framework-free logic in lib/ is covered
// here, so there is no need to pull in jsdom or a React test renderer.
export default defineConfig({
  resolve: {
    alias: {
      // Mirrors the "@/*" path mapping in tsconfig.json.
      "@": fileURLToPath(new URL("./", import.meta.url)),
    },
  },
  test: {
    environment: "node",
    include: ["lib/**/*.test.ts"],
    // Intl output depends on the ambient timezone; pin it so date assertions
    // are reproducible on CI and on developer machines alike.
    env: { TZ: "UTC" },
  },
});
