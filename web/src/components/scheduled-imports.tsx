"use client";

import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslations } from "next-intl";
import { RefreshCw } from "lucide-react";
import {
  getScheduledImports, ledgerQueries, rerunScheduledImport, type ScheduledImports as ScheduledImportsData, type ScheduledJob,
} from "@/lib/api";
import { formatDateTime, monthLabel } from "@/lib/format";
import { cn } from "@/lib/utils";
import { cellClass, footerClass, headClass, PanelHeader } from "@/components/panel";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardFooter } from "@/components/ui/card";
import { SortHead, useSort } from "@/components/sortable";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

const linkClass = "font-medium underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring";

const stateOrder = ["pending", "requesting", "polling", "downloading", "validating", "committing", "completed", "failed", "canceled"] as const;
type State = typeof stateOrder[number];
const isState = (state: string): state is State => (stateOrder as readonly string[]).includes(state);

const finished = new Set(["completed", "failed", "canceled"]);

// Months sort by period and states in the order a job moves through them.
const jobKeys = {
  period: (job: ScheduledJob) => job.period,
  state: (job: ScheduledJob) => isState(job.state) ? stateOrder.indexOf(job.state) : stateOrder.length,
};

export function monthName(period: string): string {
  const [year, month] = period.split("-").map(Number);
  return year && month ? monthLabel(period) : period;
}

function Details({ job }: { job: ScheduledJob }) {
  const t = useTranslations("scheduledImports");
  if (job.state === "failed" || job.state === "canceled") return job.errorDetail ?? t("noReport");
  if (finished.has(job.state)) return t("finished", { date: formatDateTime(job.updatedAt) });
  const next = t("nextStep", { date: formatDateTime(job.nextAttemptAt) });
  if (job.retryCount === 0) return next;
  const retry = job.errorDetail ? t("retryAfter", { count: job.retryCount, error: job.errorDetail }) : t("retry", { count: job.retryCount });
  return `${retry} ${next}`;
}

// ScheduledImports shows the Trading 212 imports, one per month. The server
// syncs the open month at 06:00 every day and catches up missed months on its
// own; a month can be imported again, which only adds missing rows.
export function ScheduledImports() {
  const t = useTranslations("scheduledImports");
  const client = useQueryClient();
  const [message, setMessage] = useState({ text: "", error: false });
  const query = useQuery({
    queryKey: ["scheduled-imports"],
    // A job that finished importing since the last look added transactions,
    // so everything that reads the ledger loads again.
    queryFn: async (context) => {
      const next = await getScheduledImports(context);
      const before = client.getQueryData<ScheduledImportsData>(["scheduled-imports"]);
      const running = new Set(before?.jobs.filter((job) => !finished.has(job.state)).map((job) => job.id));
      if (next.jobs.some((job) => job.state === "completed" && running.has(job.id))) {
        for (const key of ["imports", ...ledgerQueries]) void client.invalidateQueries({ queryKey: [key] });
      }
      return next;
    },
    refetchInterval: (current) => current.state.data?.jobs.some((job) => !finished.has(job.state)) ? 15_000 : false,
  });
  const rerun = useMutation({
    mutationFn: rerunScheduledImport,
    onSuccess: (job) => {
      void client.invalidateQueries({ queryKey: ["scheduled-imports"] });
      void client.invalidateQueries({ queryKey: ["notifications"] });
      setMessage({ text: t("rerunStarted", { month: monthName(job.period) }), error: false });
    },
    onError: (error) => setMessage({ text: error.message, error: true }),
  });
  const status = query.data;
  const jobs = useSort(status?.jobs ?? [], jobKeys, { key: "period", desc: true });
  const head = { sort: jobs.sort, onSort: jobs.toggle };
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader title={t("title")} action={status && <span className="flex items-center gap-1.5 font-mono text-[0.6875rem]">
      <span aria-hidden="true" className={cn("size-1.5 rounded-full", status.enabled ? "bg-mark" : "bg-faint")} />
      {status.enabled ? t("on") : t("off")}
    </span>} />
    {query.isPending ? <div role="status" aria-label={t("loading")} className="p-3.5"><Skeleton className="h-6" /></div> :
      query.isError ? <p role="alert" className="px-3.5 py-3 text-sm text-destructive">{query.error.message}</p> : status && <>
        <p className={cn("border-b px-3.5 py-2.5 text-xs", status.enabled ? "text-muted-foreground" : "text-faint", status.jobs.length === 0 && "border-b-0")}>
          {status.enabled ? t.rich("enabled", {
            month: monthName(status.earliestPeriod), date: formatDateTime(status.nextRunAt), timezone: status.timezone,
            time: (chunks) => <span className="font-mono text-foreground">{chunks}</span>,
          }) : t.rich("disabled", { link: (chunks) => <Link className={linkClass} href="/settings/">{chunks}</Link> })}
        </p>
        {status.jobs.length > 0 && <Table>
          <TableCaption className="sr-only">{t("caption")}</TableCaption>
          <TableHeader><TableRow className="hover:bg-transparent">
            <SortHead {...head} sortKey="period" className="w-px">{t("month")}</SortHead>
            <SortHead {...head} sortKey="state" className="w-px">{t("state")}</SortHead>
            <TableHead scope="col" className={cn(headClass, "max-sm:hidden")}>{t("details")}</TableHead>
            <TableHead scope="col" className={cn(headClass, "w-px")}><span className="sr-only">{t("actions")}</span></TableHead>
          </TableRow></TableHeader>
          <TableBody>{jobs.rows.map((job) => <TableRow key={job.id} className="hover:bg-muted/40">
            <TableCell className={cn(cellClass, "align-top font-medium whitespace-nowrap")}>{monthName(job.period)}</TableCell>
            <TableCell className={cn(cellClass, "align-top")}>
              <Badge variant={job.state === "failed" || job.state === "canceled" ? "destructive" : job.state === "completed" ? "secondary" : "warning"}>
                {isState(job.state) ? t(`states.${job.state}`) : job.state}
              </Badge>
            </TableCell>
            <TableCell className={cn(cellClass, "align-top text-xs whitespace-normal text-muted-foreground max-sm:hidden")}>
              <Details job={job} />
              {job.batchId !== null && <> <Link className={linkClass} href={`/imports/?batch=${job.batchId}`}>{t("open")}</Link></>}
            </TableCell>
            <TableCell className={cn(cellClass, "align-top")}>
              {job.canRerun && <div className="flex justify-end">
                <Button size="xs" variant="outline" disabled={rerun.isPending} onClick={() => { setMessage({ text: "", error: false }); rerun.mutate(job.period); }}>
                  <RefreshCw data-icon="inline-start" aria-hidden="true" />{t("rerun")}
                </Button>
              </div>}
            </TableCell>
          </TableRow>)}</TableBody>
        </Table>}
      </>}
    <p aria-live="polite" role={message.error ? "alert" : undefined}
      className={cn("border-t px-3.5 py-2 text-xs empty:hidden", message.error ? "text-destructive" : "text-muted-foreground")}>{message.text}</p>
    <CardFooter className={footerClass}>
      {t("footer")}
    </CardFooter>
  </Card>;
}
