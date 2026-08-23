"use client";

import { Moon, Sun, SunMoon } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { useT } from "@/lib/i18n/client";

type ThemePref = "light" | "dark" | "system";

function applyTheme(pref: ThemePref) {
  const root = document.documentElement;
  if (pref === "system") {
    root.removeAttribute("data-theme");
  } else {
    root.setAttribute("data-theme", pref);
  }
  localStorage.setItem("tf-theme", pref);
}

export function ThemeToggle() {
  const [pref, setPref] = useState<ThemePref>("system");
  const [mounted, setMounted] = useState(false);
  const t = useT();

  useEffect(() => {
    const stored = (localStorage.getItem("tf-theme") as ThemePref | null) ?? "system";
    setPref(stored);
    setMounted(true);
  }, []);

  function cycle() {
    const next: ThemePref = pref === "system" ? "light" : pref === "light" ? "dark" : "system";
    setPref(next);
    applyTheme(next);
  }

  const Icon = !mounted ? SunMoon : pref === "light" ? Sun : pref === "dark" ? Moon : SunMoon;
  const label =
    pref === "light" ? t("theme.light") : pref === "dark" ? t("theme.dark") : t("theme.system");

  return (
    <Button
      onClick={cycle}
      aria-label={t("theme.toggle", { label })}
      title={label}
      // Matches the other header icon buttons: the visible square stays 32px
      // while the hit area grows to ~44px for touch (see mobile-nav.tsx).
      className="relative h-8 w-8 px-0 before:absolute before:inset-[-6px] before:content-['']"
    >
      <Icon size={16} />
    </Button>
  );
}
