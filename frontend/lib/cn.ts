import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

/**
 * Join conditional class names and let later Tailwind utilities win over
 * earlier ones from the same group. Every ui/ primitive runs its caller's
 * `className` through this so a caller can override a default (e.g. pass
 * `px-6` to a Button whose variant sets `px-3`) without fighting specificity.
 */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
