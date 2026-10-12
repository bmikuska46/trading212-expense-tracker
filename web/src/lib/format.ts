import { formatLocale } from "@/lib/locale";

export const defaultTimezone = "Europe/Bratislava";

// Formatters follow the interface language. Building one is slow, so each is
// kept per language and options; numberFormat and dateFormat return stand-ins
// that modules can create once and that pick the current language per call.
const formatters = new Map<string, Intl.NumberFormat | Intl.DateTimeFormat | Intl.Collator>();

function cached<T extends Intl.NumberFormat | Intl.DateTimeFormat | Intl.Collator>(kind: string, options: object, build: (locale: string) => T): T {
  const locale = formatLocale();
  const key = `${kind}|${locale}|${JSON.stringify(options)}`;
  let formatter = formatters.get(key);
  if (!formatter) formatters.set(key, formatter = build(locale));
  return formatter as T;
}

export function numberFormat(options: Intl.NumberFormatOptions = {}) {
  return { format: (value: number | bigint | `${number}`) => cached("number", options, (locale) => new Intl.NumberFormat(locale, options)).format(value as number) };
}

export function dateFormat(options: Intl.DateTimeFormatOptions = {}) {
  return { format: (value?: Date | number) => cached("date", options, (locale) => new Intl.DateTimeFormat(locale, options)).format(value) };
}

export function collator(options: Intl.CollatorOptions = {}) {
  return { compare: (a: string, b: string) => cached("collator", options, (locale) => new Intl.Collator(locale, options)).compare(a, b) };
}

const plainNumber = numberFormat();
const dateTime = dateFormat({ dateStyle: "medium", timeStyle: "short" });

// formatNumber writes a count with the grouping of the interface language.
export function formatNumber(value: number): string {
  return plainNumber.format(value);
}

// formatDateTime writes an ISO timestamp as a local date and time.
export function formatDateTime(value: string): string {
  return dateTime.format(new Date(value));
}

export function timezones(current?: string): string[] {
  const zones = typeof Intl.supportedValuesOf === "function" ? Intl.supportedValuesOf("timeZone") : [defaultTimezone];
  const all = new Set([...zones, defaultTimezone, "UTC"]);
  if (current) all.add(current);
  return [...all].sort();
}

// formatMoney formats the exact decimal string from the server without
// converting it to a floating point number first. Signed amounts show a plus
// on money coming in.
export function formatMoney(decimal: string, currency: string, { signed = false } = {}): string {
  try {
    return numberFormat({ style: "currency", currency, signDisplay: signed ? "exceptZero" : "auto" }).format(decimal as `${number}`);
  } catch {
    return `${decimal} ${currency}`;
  }
}

export function formatEURCents(cents: number, options: { signed?: boolean } = {}): string {
  const sign = cents < 0 ? "-" : "";
  const absolute = Math.abs(cents);
  return formatMoney(`${sign}${Math.trunc(absolute / 100)}.${String(absolute % 100).padStart(2, "0")}`, "EUR", options);
}

// todayIn returns YYYY-MM-DD for the current day in the given timezone.
export function todayIn(timezone: string): string {
  try {
    return new Intl.DateTimeFormat("en-CA", { timeZone: timezone, year: "numeric", month: "2-digit", day: "2-digit" }).format(new Date());
  } catch {
    return new Date().toISOString().slice(0, 10);
  }
}

// monthLabel names a YYYY-MM month, such as "September 2026" or "september 2026".
export function monthLabel(month: string, style: "long" | "short" = "long"): string {
  const [year, number] = month.split("-").map(Number);
  return dateFormat({ month: style, year: "numeric", timeZone: "UTC" }).format(new Date(Date.UTC(year, number - 1, 1)));
}

// shiftMonth moves a YYYY-MM month by whole months.
export function shiftMonth(month: string, by: number): string {
  const [year, number] = month.split("-").map(Number);
  const date = new Date(Date.UTC(year, number - 1 + by, 1));
  return `${date.getUTCFullYear()}-${String(date.getUTCMonth() + 1).padStart(2, "0")}`;
}

// currencyDigits is how many minor unit digits a currency has.
export function currencyDigits(currency: string): number {
  try {
    return new Intl.NumberFormat("en", { style: "currency", currency }).resolvedOptions().maximumFractionDigits ?? 2;
  } catch {
    return 2;
  }
}

// formatMinor formats whole minor units of any currency without rounding.
export function formatMinor(minor: number, currency: string, options: { signed?: boolean } = {}): string {
  const digits = currencyDigits(currency);
  const absolute = String(Math.abs(minor)).padStart(digits + 1, "0");
  const decimal = digits === 0 ? absolute : `${absolute.slice(0, -digits)}.${absolute.slice(-digits)}`;
  return formatMoney(`${minor < 0 ? "-" : ""}${decimal}`, currency, options);
}
