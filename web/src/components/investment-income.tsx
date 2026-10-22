"use client";

import Link from "next/link";
import { useState, type ReactNode } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useTranslations } from "next-intl";
import { Download, TriangleAlert } from "lucide-react";
import { getInvestmentIncome, type IncomeKind, type IncomeMethod, type InvestmentIncome as IncomeData } from "@/lib/api";
import { dateFormat, formatEURCents, formatMinor } from "@/lib/format";
import { alertClass, alertDescriptionClass, cellClass, headClass, PanelHeader } from "@/components/panel";
import { SelectCombobox } from "@/components/select-combobox";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Empty, EmptyContent, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCaption, TableCell, TableFooter, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";

type Item = IncomeData["items"][number];
type Conversion = Item["conversions"][IncomeMethod];

// The methods of § 31 ods. 2 písm. a) of Act 595/2003, in the order of the act.
const methods: IncomeMethod[] = ["monthly", "daily", "annual", "income_months"];

const kinds: IncomeKind[] = ["dividend", "interest", "cashback"];

type T = ReturnType<typeof useTranslations<"investing.income">>;

// The items table shows one tax year, so its dates leave the year out.
const dayInYear = dateFormat({ month: "short", day: "numeric", timeZone: "UTC" });

// typeLabel names a Trading 212 type such as RETURN_OF_CAPITAL.
function typeLabel(t: T, item: Item) {
  if (item.kind === "dividend" && item.type === "ORDINARY") return t("types.dividend");
  if (item.type === "INTEREST_ON_FREE_CASH") return t("types.cashInterest");
  if (item.type === "LENDING_INTEREST") return t("types.lendingInterest");
  if (item.type !== item.type.toUpperCase()) return item.type;
  const words = item.type.toLowerCase().replaceAll("_", " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

// Missing marks a value that cannot be computed yet, with the reason on hover.
function Missing({ reason }: { reason: string }) {
  return <span className="text-faint" title={reason}>-</span>;
}

function Converted({ conversion, currency }: { conversion: Conversion; currency: string }) {
  const t = useTranslations("investing.income");
  if (conversion.eurCents === null) return <Missing reason={t("noNbsRate")} />;
  const title = [currency !== "EUR" && `${conversion.rate} · ${conversion.period}`, conversion.provisional && t("provisionalHint")].filter(Boolean).join(", ");
  return <span title={title || undefined}>
    {conversion.provisional && <span className="text-spending">~</span>}{formatEURCents(conversion.eurCents)}
  </span>;
}

function Methods({ data }: { data: IncomeData }) {
  const t = useTranslations("investing.income");
  const right = cn(headClass, "text-right");
  const number = cn(cellClass, "text-right font-mono text-[0.8125rem]");
  const optional = "max-md:hidden";
  return <div className="overflow-x-auto">
    <Table>
      <TableCaption className="sr-only">{t("methodsCaption")}</TableCaption>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead scope="col" className={headClass} title={t("methodHint")}>{t("method")}</TableHead>
          {kinds.map((kind) => <TableHead key={kind} scope="col" className={cn(right, optional)}>
            {t(`kinds.${kind}`)} {data.taxPercent[kind]} %
          </TableHead>)}
          <TableHead scope="col" className={right}>{t("base")}</TableHead>
          <TableHead scope="col" className={right}>{t("tax")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>{methods.map((method) => {
        const total = data.totals.find((item) => item.method === method)!;
        const recommended = method === data.recommended;
        const missing = total.missing > 0 && <Missing reason={t("withoutRate", { count: total.missing })} />;
        return <TableRow key={method} className={cn(recommended && "bg-muted/60")}>
          <TableCell className={cn(cellClass, "whitespace-normal")}>
            <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <span className="font-medium" title={t(`methods.${method}.description`)}>{t(`methods.${method}.name`)}</span>
              {recommended && <Badge>{data.equal ? t("allEqual") : t("lowest")}</Badge>}
              {total.provisional && total.missing === 0 && <Badge variant="warning">{t("provisional")}</Badge>}
            </span>
          </TableCell>
          {kinds.map((kind) => <TableCell key={kind} className={cn(number, optional)}>
            {formatEURCents(total.bases[kind] ?? 0)}
          </TableCell>)}
          <TableCell className={number}>{missing || formatEURCents(total.baseCents)}</TableCell>
          <TableCell className={cn(number, "font-medium")}>{missing || formatEURCents(total.taxCents)}</TableCell>
        </TableRow>;
      })}</TableBody>
    </Table>
  </div>;
}

function Items({ data }: { data: IncomeData }) {
  const t = useTranslations("investing.income");
  const right = cn(headClass, "text-right");
  const number = cn(cellClass, "text-right font-mono text-[0.8125rem]");
  const taxable = data.items.filter((item) => item.taxable);
  const informative = taxable.reduce((sum, item) => sum + (item.informativeEurCents ?? 0), 0);
  const informativeMissing = taxable.some((item) => item.informativeEurCents === null);
  // The box scrolls both ways so the header stays on top; the table's own
  // container would otherwise take the horizontal scroll and the sticky header.
  return <div className="max-h-[32rem] overflow-auto [&>[data-slot=table-container]]:overflow-visible">
    <Table>
      <TableCaption className="sr-only">{t("itemsCaption")}</TableCaption>
      <TableHeader className="sticky top-0 z-10 bg-card">
        <TableRow className="hover:bg-transparent">
          <TableHead scope="col" className={headClass}>{t("date")}</TableHead>
          <TableHead scope="col" className={headClass}>{t("payment")}</TableHead>
          <TableHead scope="col" className={right} title={t("amountHint")}>{t("amount")}</TableHead>
          <TableHead scope="col" className={right} title={t("informativeHint")}>{t("informative")}</TableHead>
          {methods.map((method) => <TableHead key={method} scope="col" className={right} title={t(`methods.${method}.description`)}>{t(`methods.${method}.short`)}</TableHead>)}
        </TableRow>
      </TableHeader>
      <TableBody>{data.items.map((item) => {
        const detail = [typeLabel(t, item), item.name, item.quantity && item.perShare && `${item.quantity} × ${item.perShare} ${item.currency}`].filter(Boolean).join(" · ");
        const received = item.receivedCurrency !== item.currency || item.receivedMinor !== item.amountMinor;
        return <TableRow key={item.id} className={cn(!item.taxable && "text-muted-foreground")}>
          <TableCell className={cn(cellClass, "font-mono text-xs text-muted-foreground")}>
            <time dateTime={item.paidOn}>{dayInYear.format(Date.parse(`${item.paidOn}T00:00:00Z`))}</time>
          </TableCell>
          <TableCell className={cellClass} title={detail}>
            <span className="flex items-center gap-2">
              <span className="max-w-48 truncate font-medium">
                {item.kind === "dividend" ? item.ticker?.replace(/_[A-Z]{2}_EQ$|_EQ$/, "") ?? t("types.dividend") : typeLabel(t, item)}
              </span>
              {!item.taxable && <Badge variant="outline">{t("notTaxed")}</Badge>}
            </span>
          </TableCell>
          <TableCell className={number} title={received ? t("received", { amount: formatMinor(item.receivedMinor, item.receivedCurrency) }) : undefined}>
            {formatMinor(item.amountMinor, item.currency)}
          </TableCell>
          <TableCell className={cn(number, "text-muted-foreground")}>
            {item.informativeEurCents === null
              ? <Missing reason={t("noCachedRate")} />
              : <span title={item.currency !== "EUR" ? `${item.informativeRate} · ${item.informativeRateDate}` : undefined}>{formatEURCents(item.informativeEurCents)}</span>}
          </TableCell>
          {methods.map((method) => <TableCell key={method} className={number}>
            <Converted conversion={item.conversions[method]} currency={item.currency} />
          </TableCell>)}
        </TableRow>;
      })}</TableBody>
      {taxable.length > 1 && <TableFooter className="bg-muted/60">
        <TableRow>
          <TableCell className={cellClass} colSpan={3}>{t("taxableTotal")}</TableCell>
          <TableCell className={cn(number, "text-muted-foreground")}>
            {informativeMissing ? <Missing reason={t("someMissing")} /> : formatEURCents(informative)}
          </TableCell>
          {methods.map((method) => {
            const total = data.totals.find((item) => item.method === method)!;
            return <TableCell key={method} className={number}>
              {total.missing > 0 ? <Missing reason={t("withoutRate", { count: total.missing })} /> : formatEURCents(total.baseCents)}
            </TableCell>;
          })}
        </TableRow>
      </TableFooter>}
    </Table>
  </div>;
}

function Problem({ title, detail }: { title: string; detail: string }) {
  return <Alert variant="destructive" className={alertClass}>
    <TriangleAlert aria-hidden="true" />
    <AlertTitle>{title}</AlertTitle>
    <AlertDescription className={alertDescriptionClass}>{detail}</AlertDescription>
  </Alert>;
}

export function InvestmentIncome() {
  const t = useTranslations("investing.income");
  const investing = useTranslations("investing");
  const [year, setYear] = useState<number | null>(null);
  const income = useQuery({
    queryKey: ["investment-income", year],
    queryFn: ({ signal }) => getInvestmentIncome(year, { signal }),
    placeholderData: keepPreviousData,
    refetchInterval: 5 * 60_000,
  });
  const data = income.data;
  let body: ReactNode = null;
  if (income.isPending) body = <Skeleton role="status" aria-label={t("loading")} className="m-3.5 h-40" />;
  else if (income.isError && !data) body = <div role="alert" className="flex flex-col items-start gap-3 p-3.5">
    <p className="font-medium text-destructive">{income.error.message}</p>
    <Button variant="outline" size="sm" onClick={() => void income.refetch()}>{investing("tryAgain")}</Button>
  </div>;
  else if (data && data.items.length === 0) body = <Empty className="border-0">
    <EmptyHeader>
      <EmptyTitle>{!data.configured ? t("connect") : data.sync.historyAttemptAt ? t("nothing", { year: String(data.year) }) : t("importing")}</EmptyTitle>
    </EmptyHeader>
    {!data.configured && <EmptyContent className="flex-row justify-center"><Button asChild><Link href="/settings/">{t("openSettings")}</Link></Button></EmptyContent>}
  </Empty>;
  else if (data) body = <>
    <Methods data={data} />
    <div className="border-t"><Items data={data} /></div>
  </>;

  return <section aria-labelledby="investment-income" className="flex flex-col gap-3">
    {data?.sync.historyError && <Problem title={t("historyError")} detail={data.sync.historyError} />}
    {data?.sync.cashbackError && <Problem title={t("cashbackError")} detail={data.sync.cashbackError} />}
    {data?.sync.ratesError && <Problem title={t("ratesError")} detail={data.sync.ratesError} />}
    <Card size="sm" aria-busy={income.isFetching} className={cn("gap-0 py-0 transition-opacity", income.isPlaceholderData && "opacity-60")}>
      <PanelHeader id="investment-income" title={t("title")} count={data?.items.length}
        action={data && <>
          {data.items.length > 0 && <Button variant="outline" size="sm" asChild>
            <a href={`/api/investing/income.csv?year=${data.year}`} download><Download aria-hidden="true" />CSV</a>
          </Button>}
          <label htmlFor="investment-income-year" className="sr-only">{t("year")}</label>
          <SelectCombobox id="investment-income-year" size="sm" className="w-24 font-mono" value={String(data.year)}
            onChange={(value) => setYear(Number(value))} options={data.years.map((value) => ({ value: String(value), label: String(value) }))} />
        </>} />
      {body}
    </Card>
  </section>;
}
