"use client";

import dynamic from "next/dynamic";
import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useState, type FormEvent, type ReactNode } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslations } from "next-intl";
import { CalendarDays, Pencil, Plus, Trash2, TriangleAlert } from "lucide-react";
import {
  ApiError, createInvestingGroup, deleteInvestingGroup, getInvesting, updateInvestingGroup, updateInvestingSettings,
  type Investing as InvestingData, type InvestingGroup, type InvestingGroupInput, type InvestingRange,
} from "@/lib/api";
import { collator, currencyDigits, dateFormat, formatMinor, numberFormat } from "@/lib/format";
import type { Series } from "@/components/investing-charts";
import { PageToolbar } from "@/components/page-toolbar";
import { SortHead, useSort } from "@/components/sortable";
import { alertClass, alertDescriptionClass, cellClass, headClass, PanelHeader } from "@/components/panel";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardFooter } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldError, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCaption, TableCell, TableFooter, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { cn } from "@/lib/utils";

function ChartPlaceholder() {
  const t = useTranslations("investing");
  return <Skeleton role="status" aria-label={t("loadingChart")} className="h-60 w-full" />;
}

const LineChart = dynamic(() => import("@/components/investing-charts").then((mod) => mod.LineChart),
  { ssr: false, loading: () => <ChartPlaceholder /> });

type Snapshot = InvestingData["snapshots"][number];
type PieValue = Snapshot["pies"][number];
type Pie = { id: number; name: string; color: string; dashed: boolean };

const ranges: InvestingRange[] = ["7d", "30d", "90d", "1y", "all"];

const percent = numberFormat({ style: "percent", minimumFractionDigits: 2, maximumFractionDigits: 2, signDisplay: "exceptZero" });
const axisPercent = numberFormat({ style: "percent", maximumFractionDigits: 1 });
const moment = dateFormat({ month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
const years = numberFormat({ maximumFractionDigits: 1 });
const yearMs = 365.25 * 86_400_000;
const sortNames = collator();

// Money in charts is in minor units. Axis labels are rounded for reading;
// exact amounts are in the tooltip, tiles and tables.
function moneyFormat(currency: string) {
  const factor = 10 ** currencyDigits(currency);
  const compact = numberFormat({ style: "currency", currency, notation: "compact", maximumFractionDigits: 1 });
  return {
    exact: (minor: number) => formatMinor(Math.round(minor), currency),
    signed: (minor: number) => formatMinor(Math.round(minor), currency, { signed: true }),
    axis: (minor: number) => compact.format(minor / factor),
  };
}
type Money = ReturnType<typeof moneyFormat>;

const share = (part: number, whole: number) => whole > 0 ? part / whole : null;

// Pies keep the color of their place among every pie ever seen, so a pie
// looks the same in every range. Past eight, pies share a dashed muted line.
function piePalette(data: InvestingData, fallback: (id: number) => string): Pie[] {
  return data.pies.map((pie, index) => ({
    id: pie.id,
    name: pie.name ?? fallback(pie.id),
    color: index < 8 ? `var(--series-${index + 1})` : "var(--muted-foreground)",
    dashed: index >= 8,
  }));
}

// rangeReturn is the time weighted return of the pies at each snapshot, or
// null before any of them appear. Each step's gain change is divided by the
// value it started from, then the steps are chained. Deposits and
// withdrawals move value and invested together and leave the gain alone, so
// they do not count as return.
function rangeReturn(ids: number[], rows: Map<number, PieValue>[]): (number | null)[] {
  const last = new Map<number, PieValue>();
  let growth = 1;
  return rows.map((row) => {
    let gain = 0;
    let base = 0;
    let seen = false;
    for (const id of ids) {
      const now = row.get(id);
      if (!now) continue;
      const then = last.get(id);
      if (then) {
        gain += now.resultMinor - then.resultMinor;
        base += then.valueMinor;
      }
      last.set(id, now);
      seen = true;
    }
    if (base > 0) growth *= 1 + gain / base;
    return seen ? growth - 1 : null;
  });
}

const latestOf = (values: (number | null)[]) => values.findLast((value) => value !== null) ?? null;

function SeriesSwatch({ color, dashed }: { color: string; dashed?: boolean }) {
  return <span aria-hidden="true" className={cn("inline-block size-2 shrink-0 rounded-[2px]", dashed && "border")}
    style={dashed ? { borderColor: color } : { background: color }} />;
}

function Legend({ items }: { items: { label: string; color: string }[] }) {
  const t = useTranslations("investing");
  return <ul className="flex flex-wrap gap-x-3.5 gap-y-1 text-xs text-muted-foreground" aria-label={t("legend")}>
    {items.map((item) => <li key={item.label} className="flex items-center gap-1.5"><SeriesSwatch color={item.color} />{item.label}</li>)}
  </ul>;
}

function Tile({ label, value, detail }: { label: string; value: ReactNode; detail: ReactNode }) {
  return <Card size="sm" className="px-3.5 py-3 md:px-4 md:py-3.5">
    <dl className="flex flex-col gap-1">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="font-mono text-[1.1875rem] leading-tight font-medium tracking-tight whitespace-nowrap md:text-xl">{value}</dd>
      <dd className="flex min-h-5 items-center gap-1 truncate font-mono text-[0.6875rem] text-faint md:text-xs">{detail}</dd>
    </dl>
  </Card>;
}

const b = (chunks: ReactNode) => <b className="font-medium text-muted-foreground">{chunks}</b>;

// StartDate sets the day of the first deposit, which the average yearly
// return counts from.
function StartDate({ startedOn }: { startedOn: string | null }) {
  const t = useTranslations("investing.start");
  const common = useTranslations("common");
  const client = useQueryClient();
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState(startedOn ?? "");
  const save = useMutation({
    mutationFn: (next: string | null) => updateInvestingSettings({ startedOn: next }),
    onSuccess: () => { void client.invalidateQueries({ queryKey: ["investing"] }); setOpen(false); },
  });
  const error = save.error instanceof ApiError ? save.error.fields.startedOn ?? save.error.message : save.error ? common("errors.generic") : "";
  const submit = (event: FormEvent) => {
    event.preventDefault();
    save.mutate(value || null);
  };
  return <Popover open={open} onOpenChange={(next) => { setOpen(next); if (next) { setValue(startedOn ?? ""); save.reset(); } }}>
    <PopoverTrigger asChild>
      <Button type="button" variant="ghost" size="icon-xs" className="-my-1 text-faint"
        aria-label={startedOn ? t("change") : t("set")}>
        {startedOn ? <Pencil aria-hidden="true" /> : <CalendarDays aria-hidden="true" />}
      </Button>
    </PopoverTrigger>
    <PopoverContent align="start" className="w-72">
      <form noValidate onSubmit={submit} className="flex flex-col gap-3">
        <Field data-invalid={Boolean(error)}>
          <FieldLabel htmlFor="investing-start">{t("label")}</FieldLabel>
          <Input id="investing-start" type="date" value={value} aria-invalid={Boolean(error)} onChange={(event) => setValue(event.target.value)} />
          <p className="text-xs text-muted-foreground">{t("hint")}</p>
          {error && <FieldError className="text-xs">{error}</FieldError>}
        </Field>
        <div className="flex gap-2">
          <Button type="submit" size="sm" disabled={save.isPending}>{save.isPending ? common("saving") : t("save")}</Button>
          {startedOn && <Button type="button" size="sm" variant="ghost" disabled={save.isPending} onClick={() => save.mutate(null)}>{t("clear")}</Button>}
        </div>
      </form>
    </PopoverContent>
  </Popover>;
}

// yearsSince is the time from the first deposit to a snapshot, or null when
// no start date is set or the snapshot is not after it.
function yearsSince(startedOn: string | null, time: number) {
  if (!startedOn) return null;
  const elapsed = (time - Date.parse(`${startedOn}T00:00:00Z`)) / yearMs;
  return elapsed > 0 ? elapsed : null;
}

function yearlyReturn(snapshot: Snapshot, startedOn: string | null) {
  const elapsed = yearsSince(startedOn, Date.parse(snapshot.at));
  const total = share(snapshot.unrealizedMinor, snapshot.costBasisMinor);
  return elapsed === null || total === null ? null : total / elapsed;
}

function Tiles({ data, rows, money }: { data: InvestingData; rows: Snapshot[]; money: Money }) {
  const t = useTranslations("investing.tiles");
  const now = rows[rows.length - 1];
  const first = rows[0];
  const delta = now.totalValueMinor - first.totalValueMinor;
  const deltaShare = share(delta, Math.abs(first.totalValueMinor));
  const onCost = share(now.unrealizedMinor, now.costBasisMinor);
  const yearly = yearlyReturn(now, data.startedOn);
  const elapsed = yearsSince(data.startedOn, Date.parse(now.at));
  const cash = now.cashAvailableMinor + now.cashInPiesMinor + now.cashReservedMinor;
  return <section aria-labelledby="investing-totals">
    <h2 id="investing-totals" className="sr-only">{t("heading")}</h2>
    <div className="grid grid-cols-2 gap-2 md:grid-cols-3 md:gap-3 2xl:grid-cols-6">
      <Tile label={t("totalValue")} value={money.exact(now.totalValueMinor)}
        detail={rows.length > 1 ? <>{b(money.signed(delta))}{deltaShare !== null && ` ${percent.format(deltaShare)}`}</> : t("oneSnapshot")} />
      <Tile label={t("unrealized")} value={money.signed(now.unrealizedMinor)}
        detail={onCost === null ? t("nothingInvested") : t.rich("onCost", { percent: percent.format(onCost), b })} />
      <Tile label={t("yearly")} value={yearly === null ? t("notSet") : percent.format(yearly)}
        detail={<>
          <span className="truncate">{elapsed === null
            ? data.startedOn ? t("startsLater") : t("setStart")
            : t("overYears", { count: Math.round(elapsed * 10) / 10, years: years.format(elapsed) })}</span>
          <StartDate startedOn={data.startedOn} />
        </>} />
      <Tile label={t("realized")} value={money.signed(now.realizedMinor)} detail={t("fromClosed")} />
      <Tile label={t("invested")} value={money.exact(now.investmentsValueMinor)} detail={t.rich("costBasis", { amount: money.exact(now.costBasisMinor), b })} />
      <Tile label={t("cash")} value={money.exact(cash)} detail={t.rich("freeCash", { amount: money.exact(now.cashAvailableMinor), b })} />
    </div>
  </section>;
}

function ChartTabs({ title, legend, chart, table, note }: {
  title: string; legend?: ReactNode; chart: ReactNode; table: ReactNode; note: string;
}) {
  const t = useTranslations("investing");
  return <Tabs defaultValue="chart" className="gap-0">
    <Card size="sm" className="gap-0 py-0">
      <PanelHeader title={title} action={<>
        {legend && <span className="max-sm:hidden">{legend}</span>}
        <TabsList className="h-7!">
          <TabsTrigger value="chart" className="px-2 text-xs">{t("chart")}</TabsTrigger>
          <TabsTrigger value="table" className="px-2 text-xs">{t("table")}</TabsTrigger>
        </TabsList>
      </>} />
      <TabsContent value="chart" className="flex flex-col gap-2 p-3.5">
        {legend && <span className="sm:hidden">{legend}</span>}
        {chart}
        <p className="text-xs text-faint">{note}</p>
      </TabsContent>
      <TabsContent value="table" className="max-h-96 overflow-auto">{table}</TabsContent>
    </Card>
  </Tabs>;
}

function SnapshotTable({ rows, money, startedOn }: { rows: Snapshot[]; money: Money; startedOn: string | null }) {
  const t = useTranslations("investing.snapshots");
  const right = cn(headClass, "text-right");
  return <Table>
    <TableCaption className="sr-only">{t("caption")}</TableCaption>
    <TableHeader className="sticky top-0 bg-card">
      <TableRow className="hover:bg-transparent">
        <TableHead scope="col" className={headClass}>{t("taken")}</TableHead>
        {(["totalValue", "invested", "costBasis", "unrealized", "return", "yearly", "realized", "freeCash"] as const).map((key) => t(key)).map((label) =>
          <TableHead key={label} scope="col" className={right}>{label}</TableHead>)}
      </TableRow>
    </TableHeader>
    <TableBody>{rows.toReversed().map((row) => {
      const total = share(row.unrealizedMinor, row.costBasisMinor);
      const yearly = yearlyReturn(row, startedOn);
      return <TableRow key={row.at}>
        <TableCell className={cn(cellClass, "font-mono text-xs text-muted-foreground")}>{moment.format(Date.parse(row.at))}</TableCell>
        {[money.exact(row.totalValueMinor), money.exact(row.investmentsValueMinor), money.exact(row.costBasisMinor),
          money.signed(row.unrealizedMinor), total === null ? "" : percent.format(total), yearly === null ? "" : percent.format(yearly),
          money.signed(row.realizedMinor), money.exact(row.cashAvailableMinor)].map((text, index) =>
          <TableCell key={index} className={cn(cellClass, "text-right font-mono text-[0.8125rem]")}>{text}</TableCell>)}
      </TableRow>;
    })}</TableBody>
  </Table>;
}

function Portfolio({ data, rows, times, money }: { data: InvestingData; rows: Snapshot[]; times: number[]; money: Money }) {
  const t = useTranslations("investing.portfolio");
  const value: Series[] = [
    { key: "invested", label: t("invested"), color: "var(--series-2)", values: rows.map((row) => row.investmentsValueMinor) },
    { key: "cost", label: t("costBasis"), color: "var(--series-3)", values: rows.map((row) => row.costBasisMinor) },
  ];
  const profit: Series[] = [
    { key: "unrealized", label: t("profitSeries"), color: "var(--series-1)", area: true, values: rows.map((row) => row.unrealizedMinor) },
  ];
  const returns: Series[] = [
    { key: "return", label: t("returnSeries"), color: "var(--series-1)", values: rows.map((row) => share(row.unrealizedMinor, row.costBasisMinor)) },
    ...(data.startedOn ? [{ key: "yearly", label: t("yearlySeries"), color: "var(--series-2)", values: rows.map((row) => yearlyReturn(row, data.startedOn)) }] : []),
  ];
  const signedPercent = (value: number) => percent.format(value);
  const table = <SnapshotTable rows={rows} money={money} startedOn={data.startedOn} />;
  return <div className="grid gap-3 lg:grid-cols-2">
    <div className="lg:col-span-2">
      <ChartTabs title={t("title")} legend={<Legend items={value} />} table={table}
        note={t("valueNote")}
        chart={<LineChart times={times} series={value} format={money.exact} axis={money.axis} label={t("valueLabel")} />} />
    </div>
    <ChartTabs title={t("profitTitle")} table={table} note={t("profitNote")}
      chart={<LineChart times={times} series={profit} format={money.signed} axis={money.axis} zero label={t("profitLabel")} />} />
    <ChartTabs title={t("returnTitle")} legend={<Legend items={returns} />} table={table}
      note={data.startedOn ? t("returnNote") : t("returnNoteNoStart")}
      chart={<LineChart times={times} series={returns} format={signedPercent} axis={(value) => axisPercent.format(value)} zero label={t("returnLabel")} />} />
  </div>;
}

const chip = "inline-flex h-6 items-center gap-1.5 rounded-md border border-input px-2 text-xs text-muted-foreground hover:bg-muted hover:text-foreground aria-pressed:border-foreground/30 aria-pressed:bg-accent aria-pressed:text-foreground disabled:pointer-events-none disabled:opacity-50";

function Pies({ data, rows, times, money }: { data: InvestingData; rows: Snapshot[]; times: number[]; money: Money }) {
  const t = useTranslations("investing.pies");
  const [selection, setSelection] = useState<number[] | null>(null);
  const [editing, setEditing] = useState(false);
  const palette = piePalette(data, (id) => t("fallback", { id: String(id) }));
  const byPie = rows.map((row) => new Map(row.pies.map((pie) => [pie.id, pie])));
  const first = new Map<number, PieValue>();
  const latest = new Map<number, PieValue>();
  for (const row of byPie) {
    for (const [id, pie] of row) {
      if (!first.has(id)) first.set(id, pie);
      latest.set(id, pie);
    }
  }
  const inRange = palette.filter((pie) => latest.has(pie.id));
  // A selection from another range keeps only the pies this range has.
  const chosen = selection?.filter((id) => latest.has(id)) ?? [];
  const narrowed = chosen.length > 0 && chosen.length < inRange.length ? new Set(chosen) : null;
  const visible = inRange.filter((pie) => !narrowed || narrowed.has(pie.id));

  const togglePie = (id: number) => {
    // From all pies the first choice narrows to that pie; then choices toggle.
    const next = new Set(narrowed ?? []);
    if (narrowed?.has(id)) next.delete(id);
    else next.add(id);
    setSelection(next.size === 0 || next.size === inRange.length ? null : [...next]);
  };
  const groupPies = (group: InvestingGroup) => inRange.filter((pie) => group.pieIds.includes(pie.id)).map((pie) => pie.id);
  const groupShown = (group: InvestingGroup) => {
    const ids = groupPies(group);
    return ids.length > 0 && ids.length === visible.length && ids.every((id) => visible.some((pie) => pie.id === id));
  };

  return <>
    <Card size="sm" className="gap-0 py-0">
      <PanelHeader title={t("title")} count={inRange.length}
        action={<Button type="button" size="xs" variant="ghost" onClick={() => setEditing(true)} disabled={data.pies.length === 0}>{t("editGroups")}</Button>} />
      {inRange.length === 0
        ? <p className="p-3.5 text-sm text-muted-foreground">
          {data.pies.length ? t("noneInRange") : t("none")}
        </p>
        : <>
          <div className="flex flex-col gap-2 border-b px-3.5 py-2.5">
            {data.groups.length > 0 && <div role="group" aria-label={t("groups")} className="flex flex-wrap gap-1.5">
              {data.groups.map((group) => {
                const ids = groupPies(group);
                const shown = groupShown(group);
                return <button key={group.id} type="button" className={chip} aria-pressed={shown} disabled={ids.length === 0}
                  title={ids.length === 0 ? t("noPiesInRange") : undefined}
                  onClick={() => setSelection(shown || ids.length === inRange.length ? null : ids)}>{group.name}</button>;
              })}
            </div>}
            <div role="group" aria-label={t("shown")} className="flex flex-wrap gap-1.5">
              <button type="button" className={chip} aria-pressed={!narrowed} onClick={() => setSelection(null)}>{t("all")}</button>
              {inRange.map((pie) => <button key={pie.id} type="button" className={cn(chip, narrowed && !narrowed.has(pie.id) && "opacity-60")}
                aria-pressed={Boolean(narrowed?.has(pie.id))} onClick={() => togglePie(pie.id)}>
                <SeriesSwatch color={pie.color} dashed={pie.dashed} />{pie.name}
              </button>)}
            </div>
          </div>
          <PieTable pies={visible} first={first} latest={latest} byPie={byPie} money={money} narrowed={Boolean(narrowed)} />
          <CardFooter className="bg-transparent px-3.5 py-2.5 text-xs text-faint">
            {t("footer")}
          </CardFooter>
        </>}
    </Card>
    {visible.length > 0 && <PieCharts pies={visible} byPie={byPie} times={times} money={money} />}
    <GroupEditor open={editing} onOpenChange={setEditing} groups={data.groups} pies={palette} />
  </>;
}

function PieTable({ pies, first, latest, byPie, money, narrowed }: {
  pies: Pie[]; first: Map<number, PieValue>; latest: Map<number, PieValue>; byPie: Map<number, PieValue>[]; money: Money; narrowed: boolean;
}) {
  const t = useTranslations("investing.pies");
  const right = "text-right";
  const number = cn(cellClass, "text-right font-mono text-[0.8125rem]");
  const optional = "max-md:hidden";
  const line = (key: string | number, name: ReactNode, value: Omit<PieValue, "id">, change: number, inRange: number | null) => {
    const gain = share(value.resultMinor, value.investedMinor);
    return <TableRow key={key}>
      <TableCell className={cn(cellClass, "max-w-56 font-medium")}>{name}</TableCell>
      <TableCell className={number}>{money.exact(value.valueMinor)}</TableCell>
      <TableCell className={cn(number, optional)}>{money.exact(value.investedMinor)}</TableCell>
      <TableCell className={number}>{money.signed(value.resultMinor)}</TableCell>
      <TableCell className={number}>{gain === null ? "" : percent.format(gain)}</TableCell>
      <TableCell className={cn(number, optional)}>{money.signed(change)}</TableCell>
      <TableCell className={number}>{inRange === null ? "" : percent.format(inRange)}</TableCell>
      <TableCell className={cn(number, optional)}>{money.exact(value.dividendsGainedMinor)}</TableCell>
    </TableRow>;
  };
  const change = (pie: Pie) => latest.get(pie.id)!.resultMinor - first.get(pie.id)!.resultMinor;
  const sum = (key: keyof Omit<PieValue, "id">) => pies.reduce((total, pie) => total + latest.get(pie.id)![key], 0);
  const total = {
    investedMinor: sum("investedMinor"), valueMinor: sum("valueMinor"), resultMinor: sum("resultMinor"), cashMinor: sum("cashMinor"),
    dividendsGainedMinor: sum("dividendsGainedMinor"), dividendsReinvestedMinor: sum("dividendsReinvestedMinor"), dividendsInCashMinor: sum("dividendsInCashMinor"),
  };
  const totalChange = pies.reduce((sum, pie) => sum + change(pie), 0);
  const sorted = useSort(pies, {
    name: (pie) => pie.name,
    value: (pie) => latest.get(pie.id)!.valueMinor,
    invested: (pie) => latest.get(pie.id)!.investedMinor,
    gain: (pie) => latest.get(pie.id)!.resultMinor,
    gainShare: (pie) => share(latest.get(pie.id)!.resultMinor, latest.get(pie.id)!.investedMinor),
    change,
    inRange: (pie) => latestOf(rangeReturn([pie.id], byPie)),
    dividends: (pie) => latest.get(pie.id)!.dividendsGainedMinor,
  }, { key: "value", desc: true });
  const head = { sort: sorted.sort, onSort: sorted.toggle };
  const body = sorted.rows.map((pie) => line(pie.id, <span className="flex items-center gap-2"><SeriesSwatch color={pie.color} dashed={pie.dashed} />
    <span className="truncate">{pie.name}</span></span>, latest.get(pie.id)!, change(pie), latestOf(rangeReturn([pie.id], byPie))));
  return <div className="overflow-x-auto">
    <Table>
      <TableCaption className="sr-only">{t("caption")}</TableCaption>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <SortHead {...head} sortKey="name">{t("pie")}</SortHead>
          <SortHead {...head} sortKey="value" className={right}>{t("value")}</SortHead>
          <SortHead {...head} sortKey="invested" className={cn(right, optional)}>{t("invested")}</SortHead>
          <SortHead {...head} sortKey="gain" className={right}>{t("gain")}</SortHead>
          <SortHead {...head} sortKey="gainShare" className={right}>{t("gainShare")}</SortHead>
          <SortHead {...head} sortKey="change" className={cn(right, optional)}>{t("change")}</SortHead>
          <SortHead {...head} sortKey="inRange" className={right}>{t("inRange")}</SortHead>
          <SortHead {...head} sortKey="dividends" className={cn(right, optional)}>{t("dividends")}</SortHead>
        </TableRow>
      </TableHeader>
      <TableBody>{body}</TableBody>
      {pies.length > 1 && <TableFooter className="bg-muted/60">
        {line("total", narrowed ? t("selected") : t("allPies"), total, totalChange, latestOf(rangeReturn(pies.map((pie) => pie.id), byPie)))}
      </TableFooter>}
    </Table>
  </div>;
}

function PieCharts({ pies, byPie, times, money }: { pies: Pie[]; byPie: Map<number, PieValue>[]; times: number[]; money: Money }) {
  const t = useTranslations("investing.pies");
  const series = (values: (pie: Pie) => (number | null)[]): Series[] =>
    pies.map((pie) => ({ key: String(pie.id), label: pie.name, color: pie.color, dashed: pie.dashed, values: values(pie) }));
  const gainShare = series((pie) => byPie.map((row) => {
    const value = row.get(pie.id);
    return value ? share(value.resultMinor, value.investedMinor) : null;
  }));
  const inRange = series((pie) => rangeReturn([pie.id], byPie));
  const gain = series((pie) => byPie.map((row) => row.get(pie.id)?.resultMinor ?? null));
  const asPercent = { format: (value: number) => percent.format(value), axis: (value: number) => axisPercent.format(value) };
  const tabs = [
    { value: "share", label: t("gainShare"), note: t("shareNote"),
      chart: <LineChart times={times} series={gainShare} {...asPercent} zero label={t("shareLabel")} /> },
    { value: "range", label: t("inRange"), note: t("rangeNote"),
      chart: <LineChart times={times} series={inRange} {...asPercent} zero label={t("rangeLabel")} /> },
    { value: "gain", label: t("gain"), note: t("gainNote"),
      chart: <LineChart times={times} series={gain} format={money.signed} axis={money.axis} zero label={t("gainLabel")} /> },
  ];
  return <Tabs defaultValue="share" className="gap-0">
    <Card size="sm" className="gap-0 py-0">
      <PanelHeader title={t("performance")} count={pies.length} action={<TabsList className="h-7!">
        {tabs.map((tab) => <TabsTrigger key={tab.value} value={tab.value} className="px-2 text-xs">{tab.label}</TabsTrigger>)}
      </TabsList>} />
      {tabs.map((tab) => <TabsContent key={tab.value} value={tab.value} className="flex flex-col gap-2 p-3.5">
        {tab.chart}
        <p className="text-xs text-faint">{tab.note} {t("chipsHint")}</p>
      </TabsContent>)}
    </Card>
  </Tabs>;
}

// useGroupCache writes a saved group into every loaded range, so the chips
// and editor update without reloading the snapshots.
function useGroupCache() {
  const client = useQueryClient();
  return (change: (groups: InvestingGroup[]) => InvestingGroup[]) => {
    client.setQueriesData<InvestingData>({ queryKey: ["investing"] }, (old) => old && { ...old, groups: change(old.groups) });
    void client.invalidateQueries({ queryKey: ["investing"], refetchType: "none" });
  };
}

const errorText = (error: unknown, generic: string) => error instanceof ApiError
  ? Object.values(error.fields)[0] ?? error.message : generic;

function GroupEditor({ open, onOpenChange, groups, pies }: {
  open: boolean; onOpenChange: (open: boolean) => void; groups: InvestingGroup[]; pies: Pie[];
}) {
  const t = useTranslations("investing.groups");
  const common = useTranslations("common");
  const write = useGroupCache();
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: (input: InvestingGroupInput) => createInvestingGroup(input),
    onSuccess: (group) => {
      write((list) => [...list, group].sort((a, b) => sortNames.compare(a.name, b.name)));
      setName("");
    },
  });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (name.trim()) create.mutate({ name, pieIds: [] });
  };
  return <Dialog open={open} onOpenChange={(next) => { onOpenChange(next); if (!next) create.reset(); }}>
    <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>{t("title")}</DialogTitle>
        <DialogDescription>{t("description")}</DialogDescription>
      </DialogHeader>
      <form noValidate onSubmit={submit} className="flex flex-col gap-1">
        <div className="flex gap-2">
          <label htmlFor="new-investing-group" className="sr-only">{t("new")}</label>
          <Input id="new-investing-group" value={name} maxLength={100} autoComplete="off" placeholder={t("new")}
            aria-invalid={create.isError} onChange={(event) => setName(event.target.value)} />
          <Button type="submit" variant="outline" disabled={create.isPending || !name.trim()}>
            <Plus data-icon="inline-start" aria-hidden="true" />{create.isPending ? t("adding") : t("add")}
          </Button>
        </div>
        {create.isError && <p role="alert" className="text-xs text-destructive">{errorText(create.error, common("errors.generic"))}</p>}
      </form>
      {groups.length === 0
        ? <p className="text-sm text-muted-foreground">{t("empty")}</p>
        : <ul className="flex flex-col gap-3">
          {groups.map((group) => <GroupFields key={group.id} group={group} pies={pies} />)}
        </ul>}
    </DialogContent>
  </Dialog>;
}

function GroupFields({ group, pies }: { group: InvestingGroup; pies: Pie[] }) {
  const t = useTranslations("investing.groups");
  const common = useTranslations("common");
  const write = useGroupCache();
  const [name, setName] = useState(group.name);
  const [confirming, setConfirming] = useState(false);
  const update = useMutation({
    mutationFn: (input: InvestingGroupInput) => updateInvestingGroup(group.id, input),
    onSuccess: (saved) => write((list) => list.map((item) => item.id === saved.id ? saved : item)),
    onError: () => setName(group.name),
  });
  const remove = useMutation({
    mutationFn: () => deleteInvestingGroup(group.id),
    onSuccess: () => write((list) => list.filter((item) => item.id !== group.id)),
  });
  const rename = () => {
    if (!name.trim()) setName(group.name);
    else if (name.trim() !== group.name) update.mutate({ name, pieIds: group.pieIds });
  };
  const toggle = (id: number, checked: boolean) =>
    update.mutate({ name: group.name, pieIds: checked ? [...group.pieIds, id] : group.pieIds.filter((pie) => pie !== id) });
  const error = update.error ?? remove.error;
  return <li className="flex flex-col gap-2 rounded-lg border p-3">
    <div className="flex items-center gap-2">
      <label htmlFor={`investing-group-${group.id}`} className="sr-only">{t("name", { name: group.name })}</label>
      <Input id={`investing-group-${group.id}`} value={name} maxLength={100} autoComplete="off" className="h-8"
        onChange={(event) => setName(event.target.value)} onBlur={rename}
        onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); rename(); } }} />
      {confirming ? <>
        <Button type="button" size="xs" variant="destructive" disabled={remove.isPending} onClick={() => remove.mutate()}>
          {remove.isPending ? t("deleting") : t("delete")}
        </Button>
        <Button type="button" size="xs" variant="ghost" onClick={() => setConfirming(false)}>{t("keep")}</Button>
      </> : <Button type="button" size="icon-sm" variant="ghost" aria-label={t("deleteGroup", { name: group.name })} onClick={() => setConfirming(true)}>
        <Trash2 aria-hidden="true" />
      </Button>}
    </div>
    <fieldset className="grid gap-x-4 gap-y-1.5 sm:grid-cols-2">
      <legend className="sr-only">{t("pies", { name: group.name })}</legend>
      {pies.map((pie) => <div key={pie.id} className="flex min-w-0 items-center gap-2">
        <Checkbox id={`investing-group-${group.id}-${pie.id}`} checked={group.pieIds.includes(pie.id)} disabled={update.isPending}
          onCheckedChange={(checked) => toggle(pie.id, checked === true)} />
        <label htmlFor={`investing-group-${group.id}-${pie.id}`} className="flex min-w-0 items-center gap-2 text-sm">
          <SeriesSwatch color={pie.color} dashed={pie.dashed} /><span className="truncate">{pie.name}</span>
        </label>
      </div>)}
    </fieldset>
    {error && <p role="alert" className="text-xs text-destructive">{errorText(error, common("errors.generic"))}</p>}
  </li>;
}

function InvestingSkeleton() {
  const t = useTranslations("investing");
  return <div role="status" aria-label={t("loading")} className="flex flex-col gap-3">
    <div className="grid grid-cols-2 gap-2 md:grid-cols-3 md:gap-3 2xl:grid-cols-6">
      {Array.from({ length: 6 }, (_, index) => <Skeleton key={index} className="h-21 rounded-xl" />)}
    </div>
    <Skeleton className="h-80 rounded-xl" />
  </div>;
}

function Report({ data, currency, rangeName }: { data: InvestingData; currency: string; rangeName: string }) {
  const t = useTranslations("investing");
  const rows = data.snapshots;
  const times = rows.map((row) => Date.parse(row.at));
  const money = moneyFormat(currency);
  return <>
    <Tiles data={data} rows={rows} money={money} />
    <Portfolio data={data} rows={rows} times={times} money={money} />
    <Pies data={data} rows={rows} times={times} money={money} />
    <p className="text-[0.6875rem] text-faint">
      {t("footnote", { currency, count: rows.length, range: rangeName, latest: moment.format(times[times.length - 1]) })}
    </p>
  </>;
}

function readRange(value: string | null): InvestingRange {
  return ranges.find((range) => range === value) ?? "30d";
}

export function Investing() {
  const t = useTranslations("investing");
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const range = readRange(params.get("range"));
  const investing = useQuery({
    queryKey: ["investing", range],
    queryFn: ({ signal }) => getInvesting(range, { signal }),
    placeholderData: keepPreviousData,
    // Snapshots arrive hourly; a few minutes keeps an open page current.
    refetchInterval: 5 * 60_000,
  });
  const data = investing.data;
  const rows = data?.snapshots ?? [];
  const rangeName = t(`ranges.${range}.name`);
  const pick = (value: string) => {
    if (!value) return;
    router.replace(value === "30d" ? pathname : `${pathname}?range=${value}`, { scroll: false });
  };

  return <>
    <PageToolbar>
      <ToggleGroup type="single" variant="outline" size="sm" spacing={0} value={range} onValueChange={pick} aria-label={t("ranges.label")}>
        {ranges.map((item) => <ToggleGroupItem key={item} value={item} aria-label={t(`ranges.${item}.name`)} className="px-2.5 font-mono text-xs">
          {t(`ranges.${item}.label`)}
        </ToggleGroupItem>)}
      </ToggleGroup>
    </PageToolbar>

    {investing.isPending && <InvestingSkeleton />}
    {investing.isError && !data && <Card role="alert" className="items-start gap-3 px-4">
      <p className="font-medium text-destructive">{investing.error.message}</p>
      <Button variant="outline" size="sm" onClick={() => void investing.refetch()}>{t("tryAgain")}</Button>
    </Card>}
    {data && <div aria-busy={investing.isFetching}
      className={cn("flex flex-col gap-3 transition-opacity", investing.isPlaceholderData && "opacity-60")}>
      {data.lastError && <Alert variant="destructive" className={alertClass}>
        <TriangleAlert aria-hidden="true" />
        <AlertTitle>{t("failed.title")}</AlertTitle>
        <AlertDescription className={alertDescriptionClass}>
          <p>{data.lastError}</p>
          {data.lastAttemptAt && <p>{t("failed.tried", { date: moment.format(Date.parse(data.lastAttemptAt)) })}</p>}
        </AlertDescription>
      </Alert>}
      {!data.configured && rows.length > 0 && <Alert className={alertClass}>
        <AlertTitle>{t("paused.title")}</AlertTitle>
        <AlertDescription className={alertDescriptionClass}>
          <p>{t.rich("paused.body", { link: (chunks) => <Link href="/settings/" className="underline underline-offset-4">{chunks}</Link> })}</p>
        </AlertDescription>
      </Alert>}
      {rows.length === 0 && <Empty className="border">
        <EmptyHeader>
          <EmptyTitle>{data.configured ? t("empty.none", { range: rangeName }) : t("empty.connect")}</EmptyTitle>
          <EmptyDescription>
            {!data.configured
              ? t("empty.connectBody")
              : data.lastAttemptAt
                ? t("empty.hourly")
                : t("empty.first")}
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent className="flex-row justify-center">
          {!data.configured && <Button asChild><Link href="/settings/">{t("empty.openSettings")}</Link></Button>}
          {data.configured && range !== "all" && <Button variant="outline" onClick={() => pick("all")}>{t("empty.showAll")}</Button>}
        </EmptyContent>
      </Empty>}
      {rows.length > 0 && data.currency && <Report data={data} currency={data.currency} rangeName={rangeName} />}
    </div>}
  </>;
}
