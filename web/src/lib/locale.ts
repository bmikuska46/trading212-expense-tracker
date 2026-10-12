// Locale is the interface language. Slovak is the default until the owner
// picks another one. The choice is kept in this browser, like the theme; the
// hooks live in components/locale.tsx.
export const locales = ["sk", "en"] as const;
export type Locale = (typeof locales)[number];

export const defaultLocale: Locale = "sk";
export const localeStorageKey = "locale";

// Each language is named in itself so the owner finds their own.
export const localeNames: Record<Locale, string> = { sk: "Slovenčina", en: "English" };

// Numbers, money and dates follow the conventions of the chosen language.
const formatLocales: Record<Locale, string> = { sk: "sk-SK", en: "en-GB" };

let current: Locale = defaultLocale;

export function isLocale(value: unknown): value is Locale {
  return locales.includes(value as Locale);
}

// setFormatLocale switches the formatters in lib/format.ts. The provider calls
// it while rendering, before any child formats a value.
export function setFormatLocale(locale: Locale) {
  current = locale;
}

export function formatLocale(): string {
  return formatLocales[current];
}

export function currentLocale(): Locale {
  return current;
}
