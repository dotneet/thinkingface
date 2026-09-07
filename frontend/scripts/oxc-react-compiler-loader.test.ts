import { createRequire } from "node:module";

import { describe, expect, it } from "vitest";

const require = createRequire(import.meta.url);
// The loader is CommonJS because Turbopack's embedded loader runner resolves
// loader paths through `require`; see the file's own header.
const loader = require("./oxc-react-compiler-loader.cjs") as (
  this: LoaderContext,
  s: string,
) => void;

interface LoaderContext {
  resourcePath: string;
  async(): (error: Error | null, code?: string, map?: unknown) => void;
  emitWarning(warning: Error): void;
}

/** Drives the loader the way the loader runner does, and resolves with its output. */
function run(resourcePath: string, source: string) {
  return new Promise<{ code: string; warnings: string[] }>((resolve, reject) => {
    const warnings: string[] = [];
    const context: LoaderContext = {
      resourcePath,
      emitWarning: (w) => warnings.push(w.message),
      async: () => (error, code) => {
        if (error) reject(error);
        else resolve({ code: code ?? "", warnings });
      },
    };
    loader.call(context, source);
  });
}

const CLIENT_COMPONENT = `"use client";

import { useState } from "react";

interface Props {
  label: string;
}

export function Counter({ label }: Props) {
  const [n, setN] = useState<number>(0);
  return (
    <button type="button" onClick={() => setN(n + 1)}>
      {label}: {n}
    </button>
  );
}
`;

describe("oxc react compiler loader", () => {
  it("memoizes a client component", async () => {
    const { code, warnings } = await run("/app/counter.tsx", CLIENT_COMPONENT);

    // The whole point: the React Compiler ran.
    expect(code).toContain('from "react/compiler-runtime"');
    expect(warnings).toEqual([]);
  });

  it("keeps the directive on the first line", async () => {
    const { code } = await run("/app/counter.tsx", CLIENT_COMPONENT);

    // Next.js reads this to place the RSC boundary. If the compiler ever
    // stopped hoisting it above its own injected import, every client
    // component in the app would silently become a Server Component.
    expect(code.split("\n")[0]).toBe('"use client";');
  });

  it("leaves JSX for Turbopack and strips the TypeScript", async () => {
    const { code } = await run("/app/counter.tsx", CLIENT_COMPONENT);

    // `jsx: "preserve"` -- Turbopack does the JSX transform, not oxc.
    expect(code).toContain("<button");
    expect(code).not.toContain("interface Props");
    expect(code).not.toContain("useState<number>");
  });

  it("passes a module the compiler cannot handle through unchanged", async () => {
    // Not valid TypeScript, so the compiler emits nothing. The build must not
    // die here: Turbopack reports the syntax error itself a moment later.
    const broken = "export function Broken( {\n";
    const { code, warnings } = await run("/app/broken.tsx", broken);

    expect(code).toBe(broken);
    expect(warnings).toHaveLength(1);
    expect(warnings[0]).toContain("/app/broken.tsx");
  });
});
