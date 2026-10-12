"use client";

import dynamic from "next/dynamic";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useState, type ReactNode } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useTranslations } from "next-intl";
import { useHotkey } from "@tanstack/react-hotkeys";
import { ArrowDownToLine, CalendarDays, CheckCircle2, ChevronLeft, ChevronRight } from "lucide-react";
import {
  ApiError, getDashboard, getStatistics, listAccounts, listTransactions, registerSearch,
  type Dashboard as DashboardData, type DashboardQuery, type Statistics, type Transaction,
} from "@/lib/api";
import type { CategorySlice } from "@/components/dashboard-charts";
import { dateFormat, formatEURCents, formatMoney, formatNumber, monthLabel, numberFormat, shiftMonth, todayIn } from "@/lib/format";
import { useSession } from "@/lib/session";
import { useIsMobile } from "@/hooks/use-mobile";
import { PageToolbar } from "@/components/page-toolbar";
import { PanelHeader } from "@/components/panel";
import { Amount, CategoryChip, TransactionBadges, categoryText, payeeInitial, payeeText, shortDate } from "@/components/transaction-bits";
import { Button } from "@/components/ui/button";
import { ButtonGroup } from "@/components/ui/button-group";
import { Card, CardFooter } from "@/components/ui/card";
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Item, ItemActions, ItemContent, ItemDescription, ItemGroup, ItemMedia, ItemTitle } from "@/components/ui/item";
import { Kbd, KbdGroup } from "@/components/ui/kbd";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectSeparator, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

function ChartPlaceholder({ height }: { height: number }) {
  const t = useTranslations("dashboard");
  return <Skeleton role="status" aria-label={t("loadingChart")} style={{ height }} className="w-full" />;
}

const TrendChart = dynamic(() => import("@/components/dashboard-charts").then((mod) => mod.TrendChart),
  { ssr: false, loading: () => <ChartPlaceholder height={232} /> });
const TrendTable = dynamic(() => import("@/components/dashboard-charts").then((mod) => mod.TrendTable),
  { ssr: false, loading: () => <ChartPlaceholder height={232} /> });
const TrendLegend = dynamic(() => import("@/components/dashboard-charts").then((mod) => mod.TrendLegend), { ssr: false });
const CategoryBars = dynamic(() => import("@/components/dashboard-charts").then((mod) => mod.CategoryBars),
  { ssr: false, loading: () => <div className="p-3.5"><ChartPlaceholder height={180} /></div> });
const CategoryPie = dynamic(() => import("@/components/dashboard-charts").then((mod) => mod.CategoryPie),
  { ssr: false, loading: () => <div className="p-3.5"><ChartPlaceholder height={184} /></div> });

function readQuery(params: URLSearchParams): DashboardQuery {
  const query: DashboardQuery = {};
  const month = params.get("month");
  const account = params.get("account");
  if (month) query.month = month;
  if (account) query.account = account;
  return query;
}

type Filters = { month: string; account: string };
type Range = { from?: string; to?: string; account?: string };

const percent = numberFormat({ style: "percent", maximumFractionDigits: 1 });
const signedPercent = numberFormat({ style: "percent", maximumFractionDigits: 1, signDisplay: "exceptZero" });
const monthShort = dateFormat({ month: "short", timeZone: "UTC" });
const shortMonth = (month: string) => monthShort.format(new Date(`${month}-01T00:00:00Z`));
const dayLong = dateFormat({ weekday: "short", month: "short", day: "numeric", timeZone: "UTC" });
const longDate = (date: string) => dayLong.format(new Date(`${date}T00:00:00Z`));

// Sparkline draws the twelve month trend behind a figure. The figure and its
// delta carry the meaning, so the line is decoration for screen readers.
function Sparkline({ values, className }: { values: number[]; className: string }) {
  const width = 96;
  const height = 34;
  if (values.length < 2) return null;
  const low = Math.min(...values);
  const high = Math.max(...values);
  const span = high - low || 1;
  const points = values.map((value, index) => [
    (index / (values.length - 1)) * (width - 4) + 2,
    height - 3 - ((value - low) / span) * (height - 6),
  ]);
  const [lastX, lastY] = points[points.length - 1];
  return <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} aria-hidden="true" className={cn("block overflow-visible", className)}>
    <polyline points={points.map(([x, y]) => `${x},${y}`).join(" ")} fill="none" stroke="currentColor" strokeWidth={1.75}
      strokeLinecap="round" strokeLinejoin="round" />
    <circle cx={lastX} cy={lastY} r={2.5} fill="currentColor" />
  </svg>;
}

function Kpi({ label, swatch, value, detail, series, stroke }: {
  label: string; swatch?: string; value: string; detail: ReactNode; series?: number[]; stroke?: string;
}) {
  return <Card size="sm" className="px-3.5 py-3 md:px-4 md:py-3.5">
    <dl className="grid grid-cols-[minmax(0,1fr)_auto] items-end gap-x-3 gap-y-1">
      <dt className="col-span-2 flex items-center gap-2 text-xs text-muted-foreground">
        {swatch && <span aria-hidden="true" className={cn("size-2 rounded-[2px]", swatch)} />}{label}
      </dt>
      <dd className="font-mono text-[1.1875rem] leading-tight font-medium tracking-tight whitespace-nowrap md:text-2xl">{value}</dd>
      {series && <dd className="col-start-2 row-span-2 row-start-2 max-md:hidden"><Sparkline values={series} className={stroke ?? ""} /></dd>}
      <dd className="col-start-1 truncate font-mono text-[0.6875rem] text-faint md:text-xs">{detail}</dd>
    </dl>
  </Card>;
}

function change(current: number, previous: number | undefined) {
  if (previous === undefined || previous === 0) return null;
  return signedPercent.format((current - previous) / Math.abs(previous));
}

function Kpis({ data }: { data: DashboardData }) {
  const t = useTranslations("dashboard.kpis");
  const trend = data.trend;
  const previous = trend.length > 1 && trend[trend.length - 1].month === data.month ? trend[trend.length - 2] : undefined;
  const incomeChange = change(data.incomeEurCents, previous?.incomeEurCents);
  const spendingChange = change(data.spendingEurCents, previous?.spendingEurCents);
  const balance = data.accounts.reduce((sum, account) => sum + account.balanceEurCents, 0);
  const moved = data.accounts.reduce((sum, account) => sum + account.changeEurCents, 0);
  const kept = data.incomeEurCents > 0 ? data.netEurCents / data.incomeEurCents : null;
  const b = (chunks: ReactNode) => <b className="font-medium text-muted-foreground">{chunks}</b>;
  const versus = (value: string | null) => value ? t.rich("change", { change: value, month: previous ? shortMonth(previous.month) : "", b })
    : previous ? t("nothingVersus", { month: shortMonth(previous.month) }) : t("nothing");

  return <section aria-labelledby="totals-heading">
    <h2 id="totals-heading" className="sr-only">{t("heading", { month: monthLabel(data.month) })}</h2>
    <div className="grid grid-cols-2 gap-2 md:gap-3 xl:grid-cols-4">
      <Kpi label={t("income")} swatch="bg-income" value={formatEURCents(data.incomeEurCents)}
        series={trend.map((month) => month.incomeEurCents)} stroke="text-income"
        detail={versus(incomeChange)} />
      <Kpi label={t("spending")} swatch="bg-spending" value={formatEURCents(data.spendingEurCents)}
        series={trend.map((month) => month.spendingEurCents)} stroke="text-spending"
        detail={<>
          {versus(spendingChange)}
          {data.refundEurCents > 0 && t("afterRefunds", { amount: formatEURCents(data.refundEurCents) })}
        </>} />
      <Kpi label={t("net")} value={formatEURCents(data.netEurCents, { signed: true })}
        series={trend.map((month) => month.netEurCents)} stroke="text-muted-foreground"
        detail={kept === null ? t("noIncome")
          : kept >= 0 ? t.rich("kept", { percent: percent.format(kept), b })
          : t.rich("over", { percent: percent.format(-kept), b })} />
      <Kpi label={data.accountId ? t("balance") : t("balanceAll")} value={formatEURCents(balance)}
        detail={<>
          {t.rich("thisMonth", { amount: formatEURCents(moved, { signed: true }), b })}
          {data.transferCount > 0 && t("transfers", { count: data.transferCount })}
        </>} />
    </div>
  </section>;
}

function TrendPanel({ data, onSelect }: { data: DashboardData; onSelect: (month: string) => void }) {
  const t = useTranslations("dashboard.trend");
  return <Tabs defaultValue="chart" className="gap-0">
    <Card size="sm" className="gap-0 py-0">
      <PanelHeader title={t("title")} count={t("months", { count: data.trend.length })} action={<>
        <span className="max-sm:hidden"><TrendLegend /></span>
        <TabsList className="h-7!">
          <TabsTrigger value="chart" className="px-2 text-xs">{t("chart")}</TabsTrigger>
          <TabsTrigger value="table" className="px-2 text-xs">{t("table")}</TabsTrigger>
        </TabsList>
      </>} />
      <TabsContent value="chart" className="flex flex-col gap-2 p-3.5">
        <span className="sm:hidden"><TrendLegend /></span>
        <TrendChart trend={data.trend} selected={data.month} onSelect={onSelect} />
        <p className="text-xs text-faint">{t("hint")}</p>
      </TabsContent>
      <TabsContent value="table" className="px-3.5 pb-2">
        <TrendTable trend={data.trend} selected={data.month} />
      </TabsContent>
    </Card>
  </Tabs>;
}

function QueueDot({ tone }: { tone: "info" | "warn" }) {
  return <span aria-hidden="true" className={cn("size-2 rounded-full ring-3",
    tone === "info" ? "bg-income ring-income/20" : "bg-spending ring-spending/20")} />;
}

function QueueItem({ tone, title, description, href, action, label }: {
  tone: "info" | "warn"; title: ReactNode; description: ReactNode; href: string; action: string; label: string;
}) {
  return <Item role="listitem" className="rounded-none border-0 border-b px-3.5 last:border-b-0">
    <ItemMedia><QueueDot tone={tone} /></ItemMedia>
    <ItemContent className="min-w-0">
      <ItemTitle>{title}</ItemTitle>
      <ItemDescription className="truncate font-mono text-[0.6875rem] text-faint">{description}</ItemDescription>
    </ItemContent>
    <ItemActions>
      <Button asChild size="xs" variant="outline">
        <Link href={href}>{action}<span className="sr-only">: {label}</span></Link>
      </Button>
    </ItemActions>
  </Item>;
}

function ReviewQueue({ data }: { data: DashboardData }) {
  const t = useTranslations("dashboard.review");
  const { review } = data;
  const waiting = review.importCount + review.needsReviewCount + review.conflictCount;
  const account = data.accountId ? String(data.accountId) : undefined;
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader title={t("title")} count={waiting} action={<span className="text-faint">{t("counted")}</span>} />
    <ItemGroup className="gap-0">
      {waiting === 0 && <Item role="listitem" className="px-3.5">
        <ItemMedia><CheckCircle2 aria-hidden="true" className="size-4 text-mark" /></ItemMedia>
        <ItemContent>
          <ItemTitle>{t("emptyTitle")}</ItemTitle>
          <ItemDescription className="text-xs">{t("emptyBody")}</ItemDescription>
        </ItemContent>
      </Item>}
      {review.imports.map((batch) => <QueueItem key={batch.id} tone="info" href={`/transactions/?batch=${batch.id}`} action={t("review")}
        label={batch.filename}
        title={t("importTitle", { count: batch.insertedCount, account: batch.account.name })}
        description={<>
          {batch.filename}
          {batch.periodStart && batch.periodEnd && t("importPeriod", { start: batch.periodStart.slice(0, 10), end: batch.periodEnd.slice(0, 10) })}
        </>} />)}
      {review.importCount > review.imports.length && <QueueItem tone="info" href="/imports/" action={t("open")}
        label={t("moreImportsLabel")}
        title={t("moreImports", { count: review.importCount - review.imports.length })}
        description={t("allImports", { count: review.importCount })} />}
      {review.needsReviewCount > 0 && <QueueItem tone="warn" action={t("open")} label={t("needsReviewLabel")}
        href={`/transactions/${registerSearch({ review: "needs_review", account })}`}
        title={t("needsReview", { count: review.needsReviewCount })}
        description={t("needsReviewBody")} />}
      {review.conflictCount > 0 && <QueueItem tone="warn" action={t("resolve")} label={t("conflictLabel")}
        href={`/transactions/${registerSearch({ review: "source_conflict", account })}`}
        title={t("conflicts", { count: review.conflictCount })}
        description={t("conflictBody")} />}
    </ItemGroup>
  </Card>;
}

function RegisterSkeleton() {
  const t = useTranslations("transactions.register");
  return <div role="status" aria-label={t("loading")} className="flex flex-col gap-3 p-3.5">
    {Array.from({ length: 6 }, (_, index) => <Skeleton key={index} className="h-6 w-full" />)}
  </div>;
}

// RecentActivity is a short, read only register for the month. Each row opens
// the transaction in the editor on the full register.
function RecentActivity({ data, range }: { data: DashboardData; range: Range }) {
  const t = useTranslations("dashboard.recent");
  const text = useTranslations("transactions");
  const columns = useTranslations("transactions.columns");
  const query = { ...range, pageSize: "10" };
  const register = useQuery({
    queryKey: ["transactions", query],
    queryFn: ({ signal }) => listTransactions(query, { signal }),
    placeholderData: keepPreviousData,
  });
  const rows = register.data?.transactions ?? [];
  const total = register.data?.total ?? 0;
  const edit = (row: Transaction) => `/transactions/${registerSearch({ ...range, edit: String(row.id) })}`;
  const days = rows.reduce<{ date: string; rows: Transaction[] }[]>((groups, row) => {
    const last = groups[groups.length - 1];
    if (last?.date === row.date) last.rows.push(row);
    else groups.push({ date: row.date, rows: [row] });
    return groups;
  }, []);

  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader title={t("title")} count={register.data ? t("count", { total: formatNumber(total), month: shortMonth(data.month) }) : undefined}
      action={<Button asChild size="xs" variant="ghost"><Link href={`/transactions/${registerSearch(range)}`}>{t("seeAll")}</Link></Button>} />
    {register.isPending && <RegisterSkeleton />}
    {register.isError && <p role="alert" className="p-3.5 text-sm text-destructive">{register.error.message}</p>}
    {register.data && rows.length === 0 && <p className="p-3.5 text-sm text-muted-foreground">{t("empty", { month: monthLabel(data.month) })}</p>}
    {rows.length > 0 && <>
      <Table className="max-md:hidden">
        <caption className="sr-only">{t("caption", { month: monthLabel(data.month) })}</caption>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            {[columns("date"), columns("payee"), columns("category"), columns("account")].map((label) => <TableHead key={label} scope="col"
              className="h-8 px-3.5 font-mono text-[0.6875rem] font-medium tracking-wider text-faint uppercase">{label}</TableHead>)}
            <TableHead scope="col" className="h-8 px-3.5 text-right font-mono text-[0.6875rem] font-medium tracking-wider text-faint uppercase">{columns("amount")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>{rows.map((row) => <TableRow key={row.id} className="relative hover:bg-muted/40 has-focus-visible:bg-muted/40">
          <TableCell className="w-px px-3.5 font-mono text-xs text-faint">{shortDate(row.date)}</TableCell>
          <TableCell className="max-w-64 px-3.5">
            <span className="flex items-center gap-2">
              <Link href={edit(row)} className="truncate font-medium outline-none after:absolute after:inset-0 hover:underline focus-visible:underline">
                {payeeText(text, row)}
              </Link>
              <TransactionBadges row={row} />
            </span>
          </TableCell>
          <TableCell className="px-3.5">
            <CategoryChip row={row} />
          </TableCell>
          <TableCell className="px-3.5 text-muted-foreground">{row.accountName}</TableCell>
          <TableCell className="px-3.5"><Amount row={row} className="text-[0.8125rem]" /></TableCell>
        </TableRow>)}</TableBody>
      </Table>
      <ul className="md:hidden" aria-label={t("list", { month: monthLabel(data.month) })}>
        {days.map((day) => <li key={day.date}>
          <h3 className="px-3.5 pt-3 pb-1.5 font-mono text-[0.6875rem] font-medium tracking-wider text-faint uppercase">{longDate(day.date)}</h3>
          <ul>{day.rows.map((row) => <li key={row.id} className="border-t">
            <Link href={edit(row)} className="grid grid-cols-[2.125rem_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-px px-3.5 py-2.5 hover:bg-muted/40">
              <span aria-hidden="true" className="row-span-2 grid size-8.5 place-items-center rounded-lg border border-input bg-muted font-mono text-[0.8125rem] font-semibold text-muted-foreground">
                {payeeInitial(text, row)}
              </span>
              <span className="flex min-w-0 items-center gap-2">
                <span className="truncate font-medium">{payeeText(text, row)}</span>
                <TransactionBadges row={row} />
              </span>
              <Amount row={row} className="row-span-2 text-sm" />
              <span className="col-start-2 truncate text-xs text-faint">{categoryText(text, row)}, {row.accountName}</span>
            </Link>
          </li>)}</ul>
        </li>)}
      </ul>
      <CardFooter className="justify-between gap-3 bg-transparent px-3.5 py-2.5 text-xs text-faint">
        <span className="font-mono">{t("shown", { shown: formatNumber(rows.length), total: formatNumber(total) })}</span>
        <Link href={`/transactions/${registerSearch(range)}`} className="flex items-center gap-2 hover:text-foreground">
          {t("openRegister")}<KbdGroup className="max-md:hidden" aria-hidden="true"><Kbd>G</Kbd><Kbd>T</Kbd></KbdGroup>
        </Link>
      </CardFooter>
    </>}
  </Card>;
}

const pieSlices = 8;
const lastDay = (month: string) => new Date(Date.parse(`${shiftMonth(month, 1)}-01T00:00:00Z`) - 86_400_000).toISOString().slice(0, 10);

// Spending periods end with the dashboard month: each of the twelve trend
// months on its own, or the last few of them together.
function spendingPeriods(data: DashboardData, t: ReturnType<typeof useTranslations<"dashboard.spending">>) {
  const months = data.trend.map((month) => month.month);
  const span = (count: number) => ({ from: `${months[months.length - count]}-01`, to: lastDay(months[months.length - 1]) });
  const ranges = [3, 6, 12].filter((count) => count <= months.length).map((count) => ({
    value: `last-${count}`, label: t("lastMonths", { count }), ...span(count),
  }));
  const single = [...months].reverse().map((month) => ({ value: month, label: monthLabel(month), from: `${month}-01`, to: lastDay(month) }));
  return { ranges, single };
}

const sliceKey = (categoryId: number | null) => categoryId === null ? "none" : String(categoryId);

// pieColors gives the categories with the most spending across all twelve
// months a fixed color each, so switching periods never repaints a category.
function pieColors(yearly: Statistics) {
  const colors = new Map<string, number>();
  yearly.categories.filter((line) => line.spendingEurCents > 0).slice(0, pieSlices)
    .forEach((line, index) => colors.set(sliceKey(line.categoryId), index));
  return colors;
}

// toSlices keeps the categories with the most spending in the period and
// folds the rest into Other. Categories whose refunds exceeded their
// expenses have no share to draw and stay out of the chart.
function toSlices(stats: Statistics, colors: Map<string, number>, names: { uncategorized: string; other: string }): CategorySlice[] {
  const spent = stats.categories.filter((line) => line.spendingEurCents > 0);
  const top = spent.slice(0, pieSlices);
  const taken = new Set(top.map((line) => colors.get(sliceKey(line.categoryId))).filter((slot) => slot !== undefined));
  const free = Array.from({ length: pieSlices }, (_, slot) => slot).filter((slot) => !taken.has(slot));
  const slices = top.map((line) => {
    const key = sliceKey(line.categoryId);
    const slot = colors.get(key) ?? free.shift() ?? 0;
    return {
      key, name: line.categoryId === null ? names.uncategorized : line.categoryName, cents: line.spendingEurCents,
      color: `var(--series-${slot + 1})`, categoryId: line.categoryId, other: false,
    };
  });
  const rest = spent.slice(pieSlices).reduce((sum, line) => sum + line.spendingEurCents, 0);
  if (rest > 0) slices.push({ key: "other", name: names.other, cents: rest, color: "var(--chart-3)", categoryId: null, other: true });
  return slices;
}

function SpendingPiePanel({ data, account }: { data: DashboardData; account?: string }) {
  const t = useTranslations("dashboard.spending");
  const { ranges, single } = spendingPeriods(data, t);
  const [value, setValue] = useState(data.month);
  const period = [...single, ...ranges].find((item) => item.value === value) ?? single[0];
  const year = ranges[ranges.length - 1];
  const query = { from: period.from, to: period.to, account };
  const yearQuery = { from: year.from, to: year.to, account };
  const stats = useQuery({
    queryKey: ["statistics", query],
    queryFn: ({ signal }) => getStatistics(query, { signal }),
    placeholderData: keepPreviousData,
  });
  const yearly = useQuery({ queryKey: ["statistics", yearQuery], queryFn: ({ signal }) => getStatistics(yearQuery, { signal }) });
  const slices = stats.data && yearly.data ? toSlices(stats.data, pieColors(yearly.data), { uncategorized: t("uncategorized"), other: t("other") }) : [];

  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader title={t("title")} action={
      <Select value={period.value} onValueChange={setValue}>
        <SelectTrigger size="sm" aria-label={t("period")} className="h-7! w-40 text-xs"><SelectValue /></SelectTrigger>
        <SelectContent align="end">
          <SelectGroup>
            <SelectLabel>{t("together")}</SelectLabel>
            {ranges.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}
          </SelectGroup>
          <SelectSeparator />
          <SelectGroup>
            <SelectLabel>{t("oneMonth")}</SelectLabel>
            {single.map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}
          </SelectGroup>
        </SelectContent>
      </Select>
    } />
    {stats.isError && <p role="alert" className="p-3.5 text-sm text-destructive">{stats.error.message}</p>}
    {!stats.isError && (!stats.data || !yearly.data) && <div className="p-3.5"><ChartPlaceholder height={184} /></div>}
    {stats.data && yearly.data && (slices.length === 0
      ? <p className="p-3.5 text-sm text-muted-foreground">{period.value.startsWith("last-") ? t("noneInLast", { count: Number(period.value.slice(5)) }) : t("none", { month: period.label })}</p>
      : <div aria-busy={stats.isFetching} className={cn("transition-opacity", stats.isPlaceholderData && "opacity-60")}>
        <CategoryPie slices={slices} label={period.label}
          href={(category) => `/transactions/${registerSearch({ from: period.from, to: period.to, account, category: String(category) })}`} />
      </div>)}
  </Card>;
}

function CategoriesPanel({ data, range }: { data: DashboardData; range: Range }) {
  const t = useTranslations("dashboard");
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader title={t("categories.title")} count={formatEURCents(data.spendingEurCents)}
      action={<Button asChild size="xs" variant="ghost"><Link href="/categories/">{t("categories.manage")}</Link></Button>} />
    {data.categories.length === 0
      ? <p className="p-3.5 text-sm text-muted-foreground">{t("spending.none", { month: monthLabel(data.month) })}</p>
      : <CategoryBars categories={data.categories} spending={data.spendingEurCents}
        href={(category) => `/transactions/${registerSearch({ ...range, category: String(category) })}`} />}
  </Card>;
}

function AccountsPanel({ data }: { data: DashboardData }) {
  const t = useTranslations("dashboard.accounts");
  const range = { from: data.from, to: data.to };
  const total = data.accounts.reduce((sum, account) => sum + account.balanceEurCents, 0);
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader title={t("title")} count={data.accounts.length}
      action={<Button asChild size="xs" variant="ghost"><Link href="/accounts/">{t("manage")}</Link></Button>} />
    <ul aria-label={t("balances", { month: monthLabel(data.month) })}>
      {data.accounts.map((account) => <li key={account.id}
        className="grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-px border-b px-3.5 py-2.5">
        <Link href={`/transactions/${registerSearch({ ...range, account: String(account.id) })}`} className="truncate font-medium hover:underline">
          {account.name}
        </Link>
        <span className="text-right font-mono text-[0.8125rem] font-medium">{formatMoney(account.balance, account.currency)}</span>
        <span className="font-mono text-[0.6875rem] text-faint">
          {account.currency}{account.archived && t("archived")}{account.currency !== "EUR" && `, ${formatEURCents(account.balanceEurCents)}`}
        </span>
        <span className="text-right font-mono text-[0.6875rem] text-faint">
          <span className="sr-only">{t("thisMonth")}</span>{formatMoney(account.change, account.currency, { signed: true })}
        </span>
      </li>)}
      <li className="grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 bg-muted/60 px-3.5 py-2.5">
        <span className="text-muted-foreground">{t("total")}</span>
        <span className="text-right font-mono text-[0.8125rem] font-medium">{formatEURCents(total)}</span>
      </li>
    </ul>
  </Card>;
}

function DashboardSkeleton() {
  const t = useTranslations("dashboard");
  return <div role="status" aria-label={t("loading")} className="grid gap-3 lg:grid-cols-12">
    <div className="grid grid-cols-2 gap-2 md:gap-3 lg:col-span-12 xl:grid-cols-4">
      {Array.from({ length: 4 }, (_, index) => <Skeleton key={index} className="h-21 rounded-xl" />)}
    </div>
    <Skeleton className="h-80 rounded-xl lg:col-span-8" />
    <Skeleton className="h-80 rounded-xl lg:col-span-4" />
  </div>;
}

function MonthControl({ month, thisMonth, invalid, onStep, onPick }: {
  month: string; thisMonth: string; invalid: boolean; onStep: (by: number) => void; onPick: (month?: string) => void;
}) {
  const t = useTranslations("dashboard.month");
  const valid = /^\d{4}-\d{2}$/.test(month);
  return <ButtonGroup aria-label={t("label")} className="flex-1 md:flex-none">
    <Tooltip>
      <TooltipTrigger asChild>
        <Button type="button" variant="outline" size="icon-sm" aria-label={t("previous")} aria-keyshortcuts="[" onClick={() => onStep(-1)}>
          <ChevronLeft aria-hidden="true" />
        </Button>
      </TooltipTrigger>
      <TooltipContent>{t("previous")} <Kbd>[</Kbd></TooltipContent>
    </Tooltip>
    <Popover>
      <PopoverTrigger asChild>
        <Button type="button" variant="outline" size="sm" aria-invalid={invalid}
          className="flex-1 font-mono tabular-nums md:min-w-26" aria-label={t("choose", { month: valid ? monthLabel(month) : month })}>
          <CalendarDays data-icon="inline-start" aria-hidden="true" className="text-muted-foreground" />
          {valid ? monthLabel(month, "short") : month}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="center" className="flex w-64 flex-col gap-3">
        <Field>
          <FieldLabel htmlFor="dashboard-month">{t("label")}</FieldLabel>
          <Input id="dashboard-month" type="month" value={valid ? month : ""} required aria-invalid={invalid}
            onChange={(event) => event.target.value && onPick(event.target.value)} />
        </Field>
        {month !== thisMonth && <Button type="button" variant="outline" size="sm" onClick={() => onPick(undefined)}>{t("current")}</Button>}
      </PopoverContent>
    </Popover>
    <Tooltip>
      <TooltipTrigger asChild>
        <Button type="button" variant="outline" size="icon-sm" aria-label={t("next")} aria-keyshortcuts="]" onClick={() => onStep(1)}>
          <ChevronRight aria-hidden="true" />
        </Button>
      </TooltipTrigger>
      <TooltipContent>{t("next")} <Kbd>]</Kbd></TooltipContent>
    </Tooltip>
  </ButtonGroup>;
}

export function Dashboard() {
  const t = useTranslations("dashboard");
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const mobile = useIsMobile();
  const query = readQuery(params);
  const timezone = useSession().data?.user?.timezone ?? "Europe/Bratislava";
  const thisMonth = todayIn(timezone).slice(0, 7);
  const accounts = useQuery({ queryKey: ["accounts"], queryFn: listAccounts });
  const dashboard = useQuery({
    queryKey: ["dashboard", query],
    queryFn: ({ signal }) => getDashboard(query, { signal }),
    placeholderData: keepPreviousData,
  });
  const data = dashboard.data;
  const month = data?.month ?? query.month ?? thisMonth;

  const apply = (changes: Partial<Filters>) => {
    const next = { ...query, ...changes };
    if (next.month === thisMonth) delete next.month;
    router.replace(`${pathname}${registerSearch(next)}`, { scroll: false });
  };
  const step = (by: number) => /^\d{4}-\d{2}$/.test(month) && apply({ month: shiftMonth(month, by) });
  useHotkey("[", () => step(-1));
  useHotkey("]", () => step(1));

  const fields = dashboard.error instanceof ApiError ? dashboard.error.fields : {};
  const range: Range = data ? { from: data.from, to: data.to, account: data.accountId ? String(data.accountId) : undefined } : {};
  const ready = data && (data.accounts.length > 0 || data.accountId);

  return <>
    <PageToolbar>
      <MonthControl month={month} thisMonth={thisMonth} invalid={Boolean(fields.month)} onStep={step}
        onPick={(next) => apply({ month: next })} />
      <Select value={query.account ?? "all"} onValueChange={(value) => apply({ account: value === "all" ? undefined : value })}>
        <SelectTrigger size="sm" aria-label={t("account")} aria-invalid={Boolean(fields.account)} className="w-36 md:w-40">
          <SelectValue />
        </SelectTrigger>
        <SelectContent align="end">
          <SelectItem value="all">{t("allAccounts")}</SelectItem>
          {accounts.data?.map((account) => <SelectItem key={account.id} value={String(account.id)}>{account.name}</SelectItem>)}
        </SelectContent>
      </Select>
    </PageToolbar>

    {dashboard.isPending && <DashboardSkeleton />}
    {dashboard.isError && !data && <Card role="alert" className="items-start gap-3 px-4">
      <p className="font-medium text-destructive">
        {Object.keys(fields).length > 0 ? t("invalid") : dashboard.error.message}
      </p>
      {Object.values(fields).map((message) => <p key={message} className="text-sm text-muted-foreground">{message}</p>)}
      <Button variant="outline" size="sm" onClick={() => router.replace(pathname)}>{t("showCurrent")}</Button>
    </Card>}
    {data && !ready && <Empty className="border">
      <EmptyHeader>
        <EmptyTitle>{t("empty.title")}</EmptyTitle>
        <EmptyDescription>{t("empty.body")}</EmptyDescription>
      </EmptyHeader>
      <EmptyContent className="flex-row justify-center">
        <Button asChild><Link href="/accounts/">{t("empty.addAccount")}</Link></Button>
        <Button asChild variant="outline"><Link href="/imports/"><ArrowDownToLine data-icon="inline-start" aria-hidden="true" />{t("empty.import")}</Link></Button>
      </EmptyContent>
    </Empty>}
    {data && ready && <div aria-busy={dashboard.isFetching}
      className={cn("flex flex-col gap-3 transition-opacity", dashboard.isPlaceholderData && "opacity-60")}>
      <Kpis data={data} />
      {mobile
        // Phones read top to bottom, so the review queue comes before the
        // chart and one breakdown shows at a time.
        ? <>
          <ReviewQueue data={data} />
          <TrendPanel data={data} onSelect={(next) => apply({ month: next })} />
          <SpendingPiePanel key={data.month} data={data} account={range.account} />
          <Tabs defaultValue="categories">
            <TabsList className="w-full">
              <TabsTrigger value="categories">{t("categories.title")}</TabsTrigger>
              <TabsTrigger value="accounts">{t("accounts.title")}</TabsTrigger>
            </TabsList>
            <TabsContent value="categories"><CategoriesPanel data={data} range={range} /></TabsContent>
            <TabsContent value="accounts"><AccountsPanel data={data} /></TabsContent>
          </Tabs>
          <RecentActivity data={data} range={range} />
        </>
        : <div className="grid gap-3 lg:grid-cols-12">
          <div className="lg:col-span-8"><TrendPanel data={data} onSelect={(next) => apply({ month: next })} /></div>
          <div className="lg:col-span-4"><ReviewQueue data={data} /></div>
          <div className="flex flex-col gap-3 lg:col-span-8">
            <SpendingPiePanel key={data.month} data={data} account={range.account} />
            <RecentActivity data={data} range={range} />
          </div>
          <div className="flex flex-col gap-3 lg:col-span-4">
            <CategoriesPanel data={data} range={range} />
            <AccountsPanel data={data} />
          </div>
        </div>}
      <p className="text-[0.6875rem] text-faint">
        {t("footnote")}
      </p>
    </div>}
  </>;
}
