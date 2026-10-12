"use client";

import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslations } from "next-intl";
import {
  confirmImport, dismissNotification, getImport, listNotifications, resendNotification, type Notification,
} from "@/lib/api";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

// useConfirmImport marks an import as reviewed. Its transactions keep their
// values and revisions; only their review status and the notification change.
export function useConfirmImport(onDone?: (message: string, error: boolean) => void) {
  const t = useTranslations("notifications");
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => confirmImport(id),
    onSuccess: (detail) => {
      client.setQueryData(["import", detail.id], detail);
      // Confirming turns categories Jev picked into payee rules.
      for (const key of ["imports", "notifications", "transactions", "transaction", "dashboard", "rules", "payees"]) void client.invalidateQueries({ queryKey: [key] });
      onDone?.(t("confirmed", { count: detail.insertedCount }), false);
    },
    onError: (error) => onDone?.(error.message, true),
  });
}

// ConfirmImport offers the confirmation for an import that still needs
// review. It shows nothing for other imports.
export function ConfirmImport({ id, report }: { id: number; report: (message: string, error: boolean) => void }) {
  const t = useTranslations("notifications");
  const query = useQuery({ queryKey: ["import", id], queryFn: ({ signal }) => getImport(id, { signal }) });
  const confirm = useConfirmImport(report);
  if (query.data?.reviewStatus !== "needs_review") return null;
  return <Alert className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-3 py-2 text-[0.8125rem]">
    <span aria-hidden="true" className="size-2 shrink-0 rounded-full bg-spending ring-3 ring-spending/20" />
    <AlertTitle className="font-mono text-[0.625rem] font-semibold tracking-wider uppercase">{t("needsReviewTitle")}</AlertTitle>
    <AlertDescription className="min-w-0 flex-1 basis-full text-[0.8125rem] sm:basis-0">
      {t.rich("needsReviewBody", {
        count: query.data.insertedCount, filename: query.data.filename,
        file: (chunks) => <span className="font-mono text-foreground">{chunks}</span>,
      })}
    </AlertDescription>
    <Button size="xs" disabled={confirm.isPending} onClick={() => confirm.mutate(id)}>
      {confirm.isPending ? t("confirming") : t("confirmImport")}
    </Button>
  </Alert>;
}

function deliveryText(item: Notification, t: ReturnType<typeof useTranslations<"notifications">>): string {
  switch (item.deliveryState) {
    case "delivered": return t("delivery.delivered");
    case "failed": return t("delivery.failed", { error: item.deliveryError ?? "" }).trim();
    case "uncertain": return item.deliveryError ?? t("delivery.uncertain");
    case "sending": return t("delivery.sending");
  }
  return item.deliveryError ? t("delivery.retry", { error: item.deliveryError }) : t("delivery.waiting");
}

function NotificationItem({ item, report }: { item: Notification; report: (message: string, error: boolean) => void }) {
  const t = useTranslations("notifications");
  const client = useQueryClient();
  const refresh = () => void client.invalidateQueries({ queryKey: ["notifications"] });
  const dismiss = useMutation({
    mutationFn: () => dismissNotification(item.id),
    onSuccess: () => { refresh(); report(t("dismissed"), false); },
    onError: (error) => report(error.message, true),
  });
  const resend = useMutation({
    mutationFn: () => resendNotification(item.id),
    onSuccess: () => { refresh(); report(t("resent"), false); },
    onError: (error) => report(error.message, true),
  });
  const failure = item.kind === "import_failure";
  const delivery = deliveryText(item, t);
  const deliveryFailed = item.deliveryState === "failed" || item.deliveryState === "uncertain";
  // The strip keeps to one line on wide screens: a status dot, the source,
  // what happened and the actions, with the email delivery state in mono.
  return <Alert variant={failure ? "destructive" : "default"}
    className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-3 py-2 text-[0.8125rem]">
    <span aria-hidden="true" className={cn("size-2 shrink-0 rounded-full ring-3",
      failure ? "bg-destructive ring-destructive/20" : "bg-spending ring-spending/20")} />
    <AlertTitle className="flex min-w-0 items-center gap-2">
      <span className="font-mono text-[0.625rem] font-semibold tracking-wider uppercase">{t(failure ? "status.failure" : "status.review")}</span>
      <span className="truncate">{item.source}</span>
    </AlertTitle>
    <AlertDescription className="min-w-0 flex-1 basis-full text-[0.8125rem] sm:basis-0">
      <span className="text-muted-foreground">
        {failure ? item.reason : t("waiting", { count: item.newTransactions })}
      </span>
      <span className={cn("ml-2 font-mono text-[0.6875rem] max-md:block max-md:ml-0", deliveryFailed ? "text-destructive" : "text-faint")}>{delivery}</span>
    </AlertDescription>
    <div className="flex items-center gap-1.5 max-sm:w-full">
      <Button asChild size="xs" variant={failure ? "outline" : "default"}>
        <Link href={item.link}>{failure ? t("openImports") : t("review")}</Link>
      </Button>
      {item.canResend && <Button size="xs" variant="outline" disabled={resend.isPending} onClick={() => resend.mutate()}>
        {resend.isPending ? t("sending") : t("sendAgain")}
      </Button>}
      {item.canDismiss && <Button size="xs" variant="ghost" disabled={dismiss.isPending} onClick={() => dismiss.mutate()}>
        {dismiss.isPending ? t("dismissing") : t("dismiss")}
      </Button>}
    </div>
  </Alert>;
}

// Notifications lists imports waiting for review and failed automatic
// imports. A review stays until its import is confirmed; a failure stays
// until it is dismissed or the month is imported again.
export function Notifications({ report }: { report: (message: string, error: boolean) => void }) {
  const t = useTranslations("notifications");
  const query = useQuery({ queryKey: ["notifications"], queryFn: listNotifications, refetchInterval: 60_000 });
  if (!query.data?.length) return null;
  return <section aria-label={t("label")} className="flex flex-col gap-2">
    {query.data.map((item) => <NotificationItem key={item.id} item={item} report={report} />)}
  </section>;
}
