"use client";

import { useLayoutEffect, useSyncExternalStore } from "react";
import { useTranslations } from "next-intl";
import { Monitor, Moon, Sun } from "lucide-react";
import { darkQuery, themeColors, themes, themeStorageKey, type Theme } from "@/lib/theme";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuLabel, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

export const themeIcons = { light: Sun, dark: Moon, system: Monitor };

function readTheme(): Theme {
  try {
    const value = localStorage.getItem(themeStorageKey);
    return value === "light" || value === "dark" ? value : "system";
  } catch {
    return "system";
  }
}

function prefersDark(): boolean {
  return window.matchMedia(darkQuery).matches;
}

const listeners = new Set<() => void>();

// subscribe reports changes from this tab, other tabs and the OS setting.
function subscribe(listener: () => void): () => void {
  const media = window.matchMedia(darkQuery);
  listeners.add(listener);
  media.addEventListener("change", listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    media.removeEventListener("change", listener);
    window.removeEventListener("storage", listener);
  };
}

function applyTheme() {
  const theme = readTheme();
  const resolved = theme === "system" ? (prefersDark() ? "dark" : "light") : theme;
  document.documentElement.classList.toggle("dark", resolved === "dark");
  // The meta tags follow the OS by media query; a chosen theme overrides both.
  document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]')
    .forEach((meta) => { meta.content = themeColors[resolved]; });
}

export function setTheme(theme: Theme) {
  try {
    if (theme === "system") localStorage.removeItem(themeStorageKey);
    else localStorage.setItem(themeStorageKey, theme);
  } catch {
    // Without storage the theme keeps following the system.
  }
  applyTheme();
  listeners.forEach((listener) => listener());
}

// useThemeSync keeps <html> in step with the saved theme. React clears the
// class the inline script set when it remounts in development, and the OS
// setting can change while the app is open.
export function useThemeSync() {
  useLayoutEffect(() => {
    applyTheme();
    return subscribe(applyTheme);
  }, []);
}

// useTheme returns the saved choice and the palette it resolves to. The
// server renders the defaults: System, resolving to light.
export function useTheme(): { theme: Theme; resolved: "light" | "dark" } {
  const theme = useSyncExternalStore(subscribe, readTheme, () => "system" as const);
  const dark = useSyncExternalStore(subscribe, prefersDark, () => false);
  return { theme, resolved: theme === "system" ? (dark ? "dark" : "light") : theme };
}

// ThemeOptions lists the themes as radio items for any dropdown menu.
export function ThemeOptions() {
  const { theme } = useTheme();
  const t = useTranslations("app.theme");
  return <DropdownMenuRadioGroup value={theme} onValueChange={(value) => setTheme(value as Theme)}>
    {themes.map((value) => {
      const Icon = themeIcons[value];
      return <DropdownMenuRadioItem key={value} value={value}><Icon aria-hidden="true" />{t(value)}</DropdownMenuRadioItem>;
    })}
  </DropdownMenuRadioGroup>;
}

// ThemeMenu is the top bar button. Its icon shows the palette in use.
export function ThemeMenu({ className }: { className?: string }) {
  const { resolved } = useTheme();
  const t = useTranslations("app.theme");
  const Icon = resolved === "dark" ? Moon : Sun;
  return <DropdownMenu>
    <DropdownMenuTrigger asChild>
      <Button variant="outline" size="icon" className={className} aria-label={t("label")}>
        <Icon aria-hidden="true" />
      </Button>
    </DropdownMenuTrigger>
    <DropdownMenuContent align="end" className="min-w-36">
      <DropdownMenuLabel className="font-normal text-muted-foreground">{t("label")}</DropdownMenuLabel>
      <ThemeOptions />
    </DropdownMenuContent>
  </DropdownMenu>;
}
