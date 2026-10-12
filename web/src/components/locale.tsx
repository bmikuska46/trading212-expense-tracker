"use client";

import { useLayoutEffect, useSyncExternalStore } from "react";
import { useTranslations } from "next-intl";
import { defaultLocale, isLocale, localeNames, locales, localeStorageKey, type Locale } from "@/lib/locale";
import { Languages } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuLabel, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

function readLocale(): Locale {
  try {
    const value = localStorage.getItem(localeStorageKey);
    return isLocale(value) ? value : defaultLocale;
  } catch {
    return defaultLocale;
  }
}

const listeners = new Set<() => void>();

// subscribe reports changes from this tab and other tabs.
function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

export function setLocale(locale: Locale) {
  try {
    if (locale === defaultLocale) localStorage.removeItem(localeStorageKey);
    else localStorage.setItem(localeStorageKey, locale);
  } catch {
    // Without storage the interface stays in the default language.
  }
  listeners.forEach((listener) => listener());
}

// useLocaleSetting returns the saved language. The static pages are rendered
// in the default language, which is also what hydration starts from.
export function useLocaleSetting(): Locale {
  return useSyncExternalStore(subscribe, readLocale, () => defaultLocale);
}

// LocaleOptions lists the languages as radio items for any dropdown menu.
export function LocaleOptions() {
  const locale = useLocaleSetting();
  return <DropdownMenuRadioGroup value={locale} onValueChange={(value) => { if (isLocale(value)) setLocale(value); }}>
    {locales.map((value) => <DropdownMenuRadioItem key={value} value={value} lang={value}>{localeNames[value]}</DropdownMenuRadioItem>)}
  </DropdownMenuRadioGroup>;
}

// LocaleMenu is the language button beside the theme button on the sign-in
// and setup pages, where the account menu is not available.
export function LocaleMenu({ className }: { className?: string }) {
  const t = useTranslations("app");
  return <DropdownMenu>
    <DropdownMenuTrigger asChild>
      <Button variant="outline" size="icon" className={className} aria-label={t("language")}>
        <Languages aria-hidden="true" />
      </Button>
    </DropdownMenuTrigger>
    <DropdownMenuContent align="end" className="min-w-36">
      <DropdownMenuLabel className="font-normal text-muted-foreground">{t("language")}</DropdownMenuLabel>
      <LocaleOptions />
    </DropdownMenuContent>
  </DropdownMenu>;
}

// useDocumentTitle names the browser tab in the interface language. The
// static page metadata carries the default language.
export function useDocumentTitle(title: string) {
  const t = useTranslations("app");
  useLayoutEffect(() => {
    document.title = t("title", { page: title });
  }, [t, title]);
}
