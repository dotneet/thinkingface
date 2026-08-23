"use client";

import { RepoErrorBoundary } from "@/components/repo/repo-error-boundary";

export default function ModelError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  return <RepoErrorBoundary kind="model" error={error} reset={reset} />;
}
