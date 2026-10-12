"use client";

import { useQuery } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { useTranslations } from "next-intl";
import { getRates, type RatesStatus } from "@/lib/api";
import { formatDateTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { footerClass, PanelHeader } from "@/components/panel";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardFooter } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";

const outcomes = ["succeeded", "transient_failure", "uncertain"] as const;

function useWhen() {
  const t = useTranslations("settings.rates");
  return (value: string | null) => value ? formatDateTime(value) : t("never");
}

function LastAttempt({ status }: { status: RatesStatus }) {
  const t = useTranslations("settings.rates");
  const when = useWhen();
  const attempt = status.lastAttempt;
  if (!attempt) return <p className="text-xs text-muted-foreground">{t("noFetch")}</p>;
  const known = outcomes.find((outcome) => outcome === attempt.outcome);
  const outcome = attempt.outcome ? known ? t(`outcomes.${known}`) : attempt.outcome : t("running");
  const variant = attempt.outcome === "succeeded" ? "secondary" : attempt.outcome ? "warning" : "outline";
  return <div className="flex flex-col gap-1 text-xs">
    <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-muted-foreground">
      {attempt.stage === "startup" ? t("lastFetchStartup") : t("lastFetchScheduled")}
      <span className="font-mono text-foreground">{when(attempt.startedAt)}</span>
      <Badge variant={variant}>{outcome}</Badge>
    </p>
    {attempt.error && <p className="text-destructive">{attempt.error}</p>}
  </div>;
}

// RateStatus shows the daily exchange rate fetch. The page never starts a
// fetch itself; the server fetches once at startup and daily at 18:00.
export function RateStatus() {
  const t = useTranslations("settings.rates");
  const when = useWhen();
  const query = useQuery({ queryKey: ["rates"], queryFn: getRates });
  const status = query.data;
  const rows: [string, string, string?][] = status ? [
    [t("currencies"), String(status.currencies)],
    [t("latest"), status.latestPublication ?? t("noneYet")],
    [t("lastSaved"), when(status.lastFetchedAt)],
    [t("nextFetch"), when(status.nextRunAt), t("daily", { timezone: status.timezone })],
    [t("bases"), status.bases.join(", "), t("basesHint")],
    [t("source"), status.endpoint],
  ] : [];
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="rates-heading" title={t("title")} action={
      <Button variant="ghost" size="icon-sm" className="size-6" disabled={query.isFetching} onClick={() => { void query.refetch(); }}
        aria-label={query.isFetching ? t("refreshing") : t("refresh")}>
        <RefreshCw aria-hidden="true" className={cn(query.isFetching && "animate-spin")} />
      </Button>} />
    <CardContent className="flex flex-col gap-3 px-3.5 py-3" aria-live="polite">
      {query.isPending ? <div role="status" aria-label={t("loading")} className="flex flex-col gap-2">
        {Array.from({ length: 6 }, (_, index) => <Skeleton key={index} className="h-4" />)}
      </div> :
        query.isError ? <p role="alert" className="text-xs text-destructive">{query.error.message}</p> : <>
          <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-xs">
            {rows.map(([label, value, note]) => <div key={label} className="contents">
              <dt className="font-mono text-[0.6875rem] tracking-wider text-faint uppercase">{label}</dt>
              <dd className="min-w-0 font-mono break-all">
                {value}
                {note && <span className="block font-sans text-faint">{note}</span>}
              </dd>
            </div>)}
          </dl>
          {status && <LastAttempt status={status} />}
        </>}
    </CardContent>
    <CardFooter className={footerClass}>
      <p>{t("footer")}</p>
    </CardFooter>
  </Card>;
}
