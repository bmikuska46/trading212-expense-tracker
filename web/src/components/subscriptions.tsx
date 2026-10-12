"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { useTranslations } from "next-intl";
import { getSubscriptions, type Subscriptions as SubscriptionsData } from "@/lib/api";
import { dateFormat, formatEURCents, formatMoney, formatNumber } from "@/lib/format";
import { cellClass, footerClass, PanelHeader } from "@/components/panel";
import { SortHead, useSort } from "@/components/sortable";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardFooter } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCaption, TableCell, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";

type Item = SubscriptionsData["subscriptions"][number];

const dayFormat = dateFormat({ month: "short", day: "numeric", timeZone: "UTC" });
const fullFormat = dateFormat({ month: "short", day: "numeric", year: "numeric", timeZone: "UTC" });
const day = 24 * 60 * 60 * 1000;

const parse = (date: string) => Date.parse(`${date}T00:00:00Z`);

function DateText({ date, withYear = false }: { date: string; withYear?: boolean }) {
  return <time dateTime={date}>{(withYear ? fullFormat : dayFormat).format(parse(date))}</time>;
}

// Due says when the next payment is expected, relative to today.
function Due({ item, today }: { item: Item; today: string }) {
  const t = useTranslations("subscriptions.due");
  const days = Math.round((parse(item.nextDue) - parse(today)) / day);
  const relative = days === 0 ? t("today") : days === 1 ? t("tomorrow") : days > 0 ? t("in", { count: days }) : t("late", { count: -days });
  return <span className="flex flex-col">
    <DateText date={item.nextDue} />
    <span className={cn("text-xs", days < 0 ? "text-spending" : "text-faint")}>{relative}</span>
  </span>;
}

function Price({ item }: { item: Item }) {
  const t = useTranslations("subscriptions");
  return <span className="flex flex-col items-end">
    <span className="font-medium">{formatMoney(item.amount, item.currency)}</span>
    {item.currency !== "EUR" && <span className="text-xs text-faint">{formatEURCents(item.eurCents)}</span>}
    {item.previousAmount && <span className="text-xs text-faint">{t("was", { amount: formatMoney(item.previousAmount, item.currency) })}</span>}
  </span>;
}

function Payee({ item }: { item: Item }) {
  return <Link href={`/transactions/?payee=${item.payeeId}`} className="truncate font-medium hover:underline focus-visible:underline">
    {item.payeeName}
  </Link>;
}

function Kpi({ label, value, detail }: { label: string; value: string; detail: string }) {
  return <Card size="sm" className="px-3.5 py-3 md:px-4 md:py-3.5">
    <dl className="flex flex-col gap-1">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="font-mono text-[1.1875rem] leading-tight font-medium tracking-tight whitespace-nowrap md:text-2xl">{value}</dd>
      <dd className="truncate font-mono text-[0.6875rem] text-faint md:text-xs">{detail}</dd>
    </dl>
  </Card>;
}

const activeKeys = {
  payee: (item: Item) => item.payeeName,
  category: (item: Item) => item.categoryName,
  account: (item: Item) => item.account.name,
  nextDue: (item: Item) => item.nextDue,
  months: (item: Item) => item.months,
  monthly: (item: Item) => item.eurCents,
};

function Active({ items, today }: { items: Item[]; today: string }) {
  const t = useTranslations("subscriptions.active");
  const { rows, sort, toggle } = useSort(items, activeKeys, { key: "monthly", desc: true });
  const head = { sort, onSort: toggle };
  const optional = "max-md:hidden";
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="active-subscriptions" title={t("title")} count={items.length} />
    {items.length === 0
      ? <p className="p-3.5 text-sm text-muted-foreground">{t("none")}</p>
      : <div className="overflow-x-auto">
        <Table aria-labelledby="active-subscriptions">
          <TableCaption className="sr-only">{t("caption")}</TableCaption>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <SortHead {...head} sortKey="payee">{t("payee")}</SortHead>
              <SortHead {...head} sortKey="category" className={optional}>{t("category")}</SortHead>
              <SortHead {...head} sortKey="account" className={optional}>{t("account")}</SortHead>
              <SortHead {...head} sortKey="nextDue">{t("nextDue")}</SortHead>
              <SortHead {...head} sortKey="months" className={cn("text-right", optional)}>{t("months")}</SortHead>
              <SortHead {...head} sortKey="monthly" className="text-right">{t("monthly")}</SortHead>
            </TableRow>
          </TableHeader>
          <TableBody>{rows.map((item) => <TableRow key={`${item.payeeId}-${item.currency}-${item.amount}`}>
            <TableCell className={cn(cellClass, "max-w-56")}>
              <span className="flex items-center gap-2">
                <Payee item={item} />
                {item.previousAmount && <Badge variant="outline">{t("newPrice")}</Badge>}
              </span>
            </TableCell>
            <TableCell className={cn(cellClass, optional, "text-muted-foreground")}>{item.categoryName}</TableCell>
            <TableCell className={cn(cellClass, optional, "text-muted-foreground")}>{item.account.name}</TableCell>
            <TableCell className={cn(cellClass, "font-mono text-[0.8125rem]")}><Due item={item} today={today} /></TableCell>
            <TableCell className={cn(cellClass, optional, "text-right font-mono text-[0.8125rem]")}
              title={t("since", { date: fullFormat.format(parse(item.since)), amount: formatEURCents(item.paidEurCents) })}>
              {item.months}
            </TableCell>
            <TableCell className={cn(cellClass, "text-right font-mono text-[0.8125rem]")}><Price item={item} /></TableCell>
          </TableRow>)}</TableBody>
        </Table>
      </div>}
    <CardFooter className={footerClass}>
      {t("footer")}
    </CardFooter>
  </Card>;
}

const endedKeys = {
  payee: (item: Item) => item.payeeName,
  months: (item: Item) => item.months,
  lastPaid: (item: Item) => item.lastPaid,
  paid: (item: Item) => item.paidEurCents,
  monthly: (item: Item) => item.eurCents,
};

function Ended({ items }: { items: Item[] }) {
  const t = useTranslations("subscriptions.ended");
  const { rows, sort, toggle } = useSort(items, endedKeys, { key: "lastPaid", desc: true });
  const head = { sort, onSort: toggle };
  const optional = "max-md:hidden";
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="ended-subscriptions" title={t("title")} count={items.length} />
    <div className="overflow-x-auto">
      <Table aria-labelledby="ended-subscriptions">
        <TableCaption className="sr-only">{t("caption")}</TableCaption>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <SortHead {...head} sortKey="payee">{t("payee")}</SortHead>
            <SortHead {...head} sortKey="months" className={optional}>{t("paid")}</SortHead>
            <SortHead {...head} sortKey="lastPaid">{t("lastPaid")}</SortHead>
            <SortHead {...head} sortKey="paid" className={cn("text-right", optional)}>{t("total")}</SortHead>
            <SortHead {...head} sortKey="monthly" className="text-right">{t("monthly")}</SortHead>
          </TableRow>
        </TableHeader>
        <TableBody>{rows.map((item) => <TableRow key={`${item.payeeId}-${item.currency}-${item.amount}`} className="text-muted-foreground">
          <TableCell className={cn(cellClass, "max-w-56")}><Payee item={item} /></TableCell>
          <TableCell className={cn(cellClass, optional)}>{t("months", { count: item.months })}</TableCell>
          <TableCell className={cn(cellClass, "font-mono text-[0.8125rem]")}><DateText date={item.lastPaid} withYear /></TableCell>
          <TableCell className={cn(cellClass, optional, "text-right font-mono text-[0.8125rem]")}>{formatEURCents(item.paidEurCents)}</TableCell>
          <TableCell className={cn(cellClass, "text-right font-mono text-[0.8125rem]")}><Price item={item} /></TableCell>
        </TableRow>)}</TableBody>
      </Table>
    </div>
  </Card>;
}

export function Subscriptions() {
  const t = useTranslations("subscriptions");
  const subscriptions = useQuery({
    queryKey: ["subscriptions"],
    queryFn: ({ signal }) => getSubscriptions({ signal }),
  });

  if (subscriptions.isPending) return <div role="status" aria-label={t("loading")} className="flex flex-col gap-3">
    <div className="grid grid-cols-2 gap-2 md:gap-3 xl:grid-cols-3">
      {[0, 1, 2].map((index) => <Skeleton key={index} className={cn("h-21", index === 2 && "max-xl:hidden")} />)}
    </div>
    <Skeleton className="h-64" />
  </div>;
  if (subscriptions.isError) return <Card role="alert" className="items-start gap-3 px-4">
    <p className="font-medium text-destructive">{subscriptions.error.message}</p>
    <Button variant="outline" size="sm" onClick={() => void subscriptions.refetch()}>{t("tryAgain")}</Button>
  </Card>;
  const data = subscriptions.data;
  if (data.subscriptions.length === 0) return <Empty className="border">
    <EmptyHeader>
      <EmptyTitle>{t("empty.title")}</EmptyTitle>
      <EmptyDescription>
        {t("empty.body")}
      </EmptyDescription>
    </EmptyHeader>
  </Empty>;

  const active = data.subscriptions.filter((item) => item.active);
  const ended = data.subscriptions.filter((item) => !item.active);
  return <div aria-busy={subscriptions.isFetching} className="flex flex-col gap-3">
    <section aria-labelledby="subscription-totals">
      <h2 id="subscription-totals" className="sr-only">{t("totals")}</h2>
      <div className="grid grid-cols-2 gap-2 md:gap-3 xl:grid-cols-3">
        <Kpi label={t("monthly")} value={formatEURCents(data.monthlyEurCents)} detail={t("activeCount", { count: active.length })} />
        <Kpi label={t("yearly")} value={formatEURCents(data.yearlyEurCents)} detail={t("todayPrices")} />
        <Kpi label={t("endedLabel")} value={formatNumber(ended.length)} detail={t("endedCount", { count: ended.length })} />
      </div>
    </section>
    <Active items={active} today={data.today} />
    {ended.length > 0 && <Ended items={ended} />}
  </div>;
}
