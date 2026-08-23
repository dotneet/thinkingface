"use client";

import { RepoErrorBoundary } from "@/components/repo/repo-error-boundary";

export default function DatasetError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  return <RepoErrorBoundary kind="dataset" error={error} reset={reset} />;
}
