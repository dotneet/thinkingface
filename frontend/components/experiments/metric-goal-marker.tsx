"use client";

import { MoveDown, MoveUp, Target, Trophy } from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n/client";
import type { MetricGoal } from "@/types/api";

/**
 * The ↓ / ↑ next to a metric's name that says which direction is better.
 *
 * For a viewer who can write it is also the way to change the goal: a metric
 * with a goal shows its arrow as a button, a metric without one shows a faint
 * target that opens the same dialog. A reader sees the arrow, or nothing.
 */
export function MetricGoalMarker({
  metric,
  goal,
  canWrite,
  onEdit,
  className,
}: {
  metric: string;
  goal: MetricGoal | undefined;
  canWrite: boolean;
  onEdit?: () => void;
  className?: string;
}) {
  const t = useT();
  const label =
    goal === "min"
      ? t("experiments.goals.markerMin", { metric })
      : goal === "max"
        ? t("experiments.goals.markerMax", { metric })
        : undefined;
  const Icon = goal === "min" ? MoveDown : goal === "max" ? MoveUp : Target;

  if (canWrite && onEdit) {
    const action = goal
      ? t("experiments.goals.changeGoalAria", { metric })
      : t("experiments.goals.setGoalAria", { metric });
    return (
      <Button
        size="sm"
        variant="ghost"
        onClick={onEdit}
        aria-label={label ? `${label}. ${action}` : action}
        title={label ?? action}
        className={cn("px-0.5 py-0.5", !goal && "opacity-40 hover:opacity-100", className)}
      >
        <Icon size={12} className={goal ? "text-fg" : undefined} />
      </Button>
    );
  }

  if (!goal || !label) return null;
  return (
    <span role="img" aria-label={label} title={label} className={cn("inline-flex", className)}>
      <Icon size={12} className="text-fg-muted" />
    </span>
  );
}

/**
 * The trophy on the run that holds the best value of a metric with a goal.
 * The server decides who that is (`best` on the run listing).
 */
export function BestRunMarker({ metric, goal }: { metric: string; goal: MetricGoal | undefined }) {
  const t = useT();
  const label =
    goal === "min"
      ? t("experiments.goals.bestMin", { metric })
      : t("experiments.goals.bestMax", { metric });
  return (
    <span role="img" aria-label={label} title={label} className="inline-flex">
      <Trophy size={12} className="text-warning" />
    </span>
  );
}
