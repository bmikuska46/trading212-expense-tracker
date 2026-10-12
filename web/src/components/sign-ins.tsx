"use client";

import { useQuery } from "@tanstack/react-query";
import { useTranslations } from "next-intl";
import { listSessions, type SignIn } from "@/lib/api";
import { formatDateTime } from "@/lib/format";
import { formatLocale } from "@/lib/locale";
import { footerClass, PanelHeader } from "@/components/panel";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardFooter } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";

// Order matters: Edge and Opera also name Chrome, and Chrome names Safari.
const browsers: [RegExp, string][] = [
  [/Edg(e|A|iOS)?\//, "Edge"], [/OPR\/|Opera/, "Opera"], [/SamsungBrowser\//, "Samsung Internet"],
  [/Firefox\/|FxiOS\//, "Firefox"], [/Chrome\/|CriOS\//, "Chrome"], [/Safari\//, "Safari"],
];
const systems: [RegExp, string][] = [
  [/iPhone/, "iOS"], [/iPad/, "iPadOS"], [/Android/, "Android"], [/Windows/, "Windows"],
  [/Mac OS X|Macintosh/, "macOS"], [/CrOS/, "ChromeOS"], [/Linux/, "Linux"],
];

// device names the browser and system in a user agent, such as "Firefox, macOS".
function device(userAgent: string): string {
  const browser = browsers.find(([pattern]) => pattern.test(userAgent))?.[1];
  const system = systems.find(([pattern]) => pattern.test(userAgent))?.[1];
  return [browser, system].filter(Boolean).join(", ");
}

function countryName(code: string): string {
  try {
    return new Intl.DisplayNames(formatLocale(), { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
}

function usePlace() {
  const t = useTranslations("settings.signIns");
  return (entry: SignIn) => {
    if (entry.country === "T1") return t("tor");
    const country = entry.country && countryName(entry.country);
    return [entry.city || entry.region, country].filter(Boolean).join(", ") || t("unknownPlace");
  };
}

// SignIns lists the signed-in user's active sessions with where and on what
// they were signed in, so an unfamiliar one stands out.
export function SignIns() {
  const t = useTranslations("settings.signIns");
  const place = usePlace();
  const query = useQuery({ queryKey: ["sessions"], queryFn: listSessions });
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="sign-ins-heading" title={t("title")} count={query.data?.length} />
    <CardContent className="px-3.5 py-3">
      {query.isPending ? <div role="status" aria-label={t("loading")} className="flex flex-col gap-2">
        {Array.from({ length: 2 }, (_, index) => <Skeleton key={index} className="h-10" />)}
      </div>
        : query.isError ? <p role="alert" className="text-xs text-destructive">{query.error.message}</p>
        : <ul aria-labelledby="sign-ins-heading" className="flex flex-col divide-y text-xs">
          {query.data.map((entry) => <li key={`${entry.createdAt}-${entry.ip}-${entry.userAgent}`} className="flex flex-col gap-0.5 py-2 first:pt-0 last:pb-0">
            <span className="flex min-w-0 items-center gap-2">
              <span className="truncate font-medium">{place(entry)}</span>
              {entry.current && <Badge variant="secondary">{t("current")}</Badge>}
            </span>
            <span className="truncate text-muted-foreground">{device(entry.userAgent) || t("unknownDevice")}</span>
            <span className="flex flex-wrap gap-x-2 font-mono text-faint">
              <span>{formatDateTime(entry.createdAt)}</span>
              {entry.ip && <span className="break-all">{entry.ip}</span>}
            </span>
          </li>)}
        </ul>}
    </CardContent>
    <CardFooter className={footerClass}>
      <p>{t("footer")}</p>
    </CardFooter>
  </Card>;
}
