import { useEffect, useState } from "react";

export type ThemeChoice = "system" | "light" | "dark";

const KEY = "clusterview.theme";

function initial(): ThemeChoice {
  const fromUrl = new URLSearchParams(window.location.search).get("theme");
  if (fromUrl === "light" || fromUrl === "dark") return fromUrl;
  try {
    const saved = localStorage.getItem(KEY);
    if (saved === "light" || saved === "dark") return saved;
  } catch {
    // storage blocked: follow the system
  }
  return "system";
}

/** Light, dark, or whatever the system says; remembered in this browser. */
export function useTheme() {
  const [theme, setTheme] = useState<ThemeChoice>(initial);
  useEffect(() => {
    const root = document.documentElement;
    if (theme === "system") delete root.dataset.theme;
    else root.dataset.theme = theme;
    try {
      if (theme === "system") localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, theme);
    } catch {
      // storage blocked: the choice lasts until reload
    }
  }, [theme]);
  const next = () => setTheme((t) => (t === "system" ? "light" : t === "light" ? "dark" : "system"));
  return { theme, next };
}
