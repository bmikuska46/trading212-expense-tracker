"use client";

// Charts load separately from the dashboard, so other pages never download
// them. They are plain SVG and HTML: one EUR axis, thin bars with rounded
// data ends, a tooltip on hover and focus, and a table view. The dashboard
// places the legend in the card header.
import Link from "next/link";
import { useLayoutEffect, useRef, useState, type KeyboardEvent } from "react";
import { useTranslations } from "next-intl";
import type { Dashboard } from "@/lib/api";
import { dateFormat, formatEURCents, monthLabel, numberFormat } from "@/lib/format";
import { cn } from "@/lib/utils";

type TrendMonth = Dashboard["trend"][number];
type CategoryLine = Dashboard["categories"][number];

function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    const observer = new ResizeObserver(([entry]) => setWidth(Math.floor(entry.contentRect.width)));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  return [ref, width] as const;
}

// Narrow screens get one-letter months; the tooltip and table name them in full.
const tickFormats = { short: dateFormat({ month: "short", timeZone: "UTC" }), narrow: dateFormat({ month: "narrow", timeZone: "UTC" }) };
const monthTick = (month: string, style: "short" | "narrow") => tickFormats[style].format(new Date(`${month}-01T00:00:00Z`));

// Axis labels are rounded for reading; exact amounts are in the tooltip and table.
const compact = numberFormat({ style: "currency", currency: "EUR", notation: "compact", maximumFractionDigits: 1 });

function ticks(values: number[]) {
  const low = Math.min(0, ...values);
  const high = Math.max(0, ...values);
  const span = high - low || 10_000;
  const raw = span / 4;
  const power = 10 ** Math.floor(Math.log10(raw));
  const unit = raw / power;
  const step = (unit <= 1 ? 1 : unit <= 2 ? 2 : unit <= 5 ? 5 : 10) * power;
  const first = Math.floor(low / step) * step;
  const last = Math.max(Math.ceil(high / step) * step, first + step);
  const list: number[] = [];
  for (let value = first; value <= last + step / 2; value += step) list.push(value);
  return list;
}

// barPath rounds only the data end of a bar and keeps the baseline square.
function barPath(x: number, base: number, end: number, width: number) {
  const height = Math.abs(base - end);
  if (height < 0.5) return "";
  const r = Math.min(4, width / 2, height);
  const up = end < base;
  const edge = up ? end + r : end - r;
  return `M${x},${base}V${edge}Q${x},${end} ${x + r},${end}H${x + width - r}Q${x + width},${end} ${x + width},${edge}V${base}Z`;
}

export function Swatch({ className }: { className: string }) {
  return <span aria-hidden="true" className={cn("inline-block size-2 shrink-0 rounded-[2px]", className)} />;
}

export function TrendLegend() {
  const t = useTranslations("dashboard.charts");
  return <ul className="flex flex-wrap gap-x-3.5 gap-y-1 text-xs text-muted-foreground" aria-label={t("legend")}>
    <li className="flex items-center gap-1.5"><Swatch className="bg-income" />{t("income")}</li>
    <li className="flex items-center gap-1.5"><Swatch className="bg-spending" />{t("spending")}</li>
  </ul>;
}

export function TrendChart({ trend, selected, onSelect }: {
  trend: TrendMonth[]; selected: string; onSelect: (month: string) => void;
}) {
  const t = useTranslations("dashboard.charts");
  const [frame, width] = useWidth<HTMLDivElement>();
  const [active, setActive] = useState<number | null>(null);
  const height = width < 480 ? 200 : 232;
  const margin = { top: 10, right: 4, bottom: 36, left: 52 };
  const plot = { width: Math.max(0, width - margin.left - margin.right), height: height - margin.top - margin.bottom };
  const scale = ticks(trend.flatMap((month) => [month.incomeEurCents, month.spendingEurCents]));
  const low = scale[0];
  const high = scale[scale.length - 1];
  const y = (value: number) => margin.top + plot.height - ((value - low) / (high - low)) * plot.height;
  const band = trend.length ? plot.width / trend.length : 0;
  const bar = Math.max(3, Math.min(14, (band * 0.6 - 2) / 2));
  const base = y(0);
  const tickStyle = band < 32 ? "narrow" : "short";
  const current = active === null ? null : trend[active];

  const move = (event: KeyboardEvent<HTMLDivElement>) => {
    const last = trend.length - 1;
    const next = { ArrowLeft: (active ?? last + 1) - 1, ArrowRight: (active ?? -1) + 1, Home: 0, End: last }[event.key];
    if (next !== undefined) {
      event.preventDefault();
      setActive(Math.min(last, Math.max(0, next)));
    } else if (event.key === "Enter" && active !== null) {
      event.preventDefault();
      onSelect(trend[active].month);
    }
  };
  const tooltipLeft = active === null ? 0 : margin.left + band * (active + 0.5);
  const align = active === null ? "" : active < 2 ? "translate-x-[-12%]" : active > trend.length - 3 ? "translate-x-[-88%]" : "-translate-x-1/2";

  return <div ref={frame} tabIndex={0} role="group" onKeyDown={move}
    aria-label={t("trendLabel")}
    onFocus={() => setActive((value) => value ?? trend.length - 1)} onBlur={() => setActive(null)}
    className="relative rounded-md focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-ring" style={{ minHeight: height }}>
    {width > 0 && <svg width={width} height={height} aria-hidden="true" className="block overflow-visible"
      onPointerLeave={() => setActive(null)}>
      {trend.map((month, index) => month.month === selected && <rect key="selected" x={margin.left + band * index} y={margin.top}
        width={band} height={plot.height} rx={4} className="fill-accent" />)}
      {scale.map((value) => <g key={value}>
        <line x1={margin.left} x2={width - margin.right} y1={y(value)} y2={y(value)}
          className={value === 0 ? "stroke-foreground/30" : "stroke-border"} strokeWidth={1} shapeRendering="crispEdges" />
        <text x={margin.left - 8} y={y(value)} dy="0.32em" textAnchor="end" className="fill-faint font-mono text-[0.6875rem]">
          {compact.format(value / 100)}
        </text>
      </g>)}
      {trend.map((month, index) => {
        const left = margin.left + band * index;
        const center = left + band / 2;
        const isSelected = month.month === selected;
        return <g key={month.month}>
          {active === index && !isSelected && <rect x={left} y={margin.top} width={band} height={plot.height} rx={4} className="fill-muted" />}
          <path d={barPath(center - bar - 1, base, y(month.incomeEurCents), bar)} className="fill-income" />
          <path d={barPath(center + 1, base, y(month.spendingEurCents), bar)} className="fill-spending" />
          <text x={center} y={height - margin.bottom + 16} textAnchor="middle"
            className={cn("font-mono text-[0.6875rem]", isSelected ? "fill-foreground font-medium" : "fill-faint")}>{monthTick(month.month, tickStyle)}</text>
          {(index === 0 || month.month.endsWith("-01")) && <text x={center} y={height - margin.bottom + 30} textAnchor="middle"
            className="fill-faint font-mono text-[0.6875rem]">{month.month.slice(0, 4)}</text>}
          <rect x={left} y={margin.top} width={band} height={plot.height + 24} fill="transparent" className="cursor-pointer"
            onPointerEnter={() => setActive(index)} onClick={() => onSelect(month.month)} />
        </g>;
      })}
    </svg>}
    {current && <div role="presentation"
      className={cn("pointer-events-none absolute top-0 z-10 w-48 rounded-md border bg-popover p-2.5 text-xs text-popover-foreground shadow-lg", align)}
      style={{ left: tooltipLeft }}>
      <p className="font-medium">{monthLabel(current.month)}</p>
      <dl className="mt-1.5 grid grid-cols-[auto_1fr] items-center gap-x-2 gap-y-1">
        <dt className="flex items-center gap-2 text-muted-foreground"><Swatch className="bg-income" />{t("income")}</dt>
        <dd className="text-right font-mono">{formatEURCents(current.incomeEurCents)}</dd>
        <dt className="flex items-center gap-2 text-muted-foreground"><Swatch className="bg-spending" />{t("spending")}</dt>
        <dd className="text-right font-mono">{formatEURCents(current.spendingEurCents)}</dd>
        <dt className="pl-4 text-muted-foreground">{t("net")}</dt>
        <dd className="text-right font-mono">{formatEURCents(current.netEurCents, { signed: true })}</dd>
      </dl>
    </div>}
    <p aria-live="polite" className="sr-only">
      {current ? t("trendLive", {
        month: monthLabel(current.month), income: formatEURCents(current.incomeEurCents),
        spending: formatEURCents(current.spendingEurCents), net: formatEURCents(current.netEurCents),
      }) : ""}
    </p>
  </div>;
}

export function TrendTable({ trend, selected }: { trend: TrendMonth[]; selected: string }) {
  const t = useTranslations("dashboard.charts");
  return <div className="overflow-x-auto">
    <table className="w-full text-sm">
      <caption className="sr-only">{t("tableCaption")}</caption>
      <thead><tr className="border-b text-left font-mono text-[0.6875rem] tracking-wider text-faint uppercase">
        <th scope="col" className="py-2 pr-3 font-medium">{t("month")}</th>
        <th scope="col" className="py-2 pr-3 text-right font-medium">{t("income")}</th>
        <th scope="col" className="py-2 pr-3 text-right font-medium">{t("spending")}</th>
        <th scope="col" className="py-2 text-right font-medium">{t("net")}</th>
      </tr></thead>
      <tbody>{trend.map((month) => <tr key={month.month} aria-current={month.month === selected ? "date" : undefined}
        className="border-b last:border-0 aria-[current]:bg-accent">
        <th scope="row" className="py-1.5 pr-3 text-left font-normal text-muted-foreground">{monthLabel(month.month, "short")}</th>
        <td className="py-1.5 pr-3 text-right font-mono">{formatEURCents(month.incomeEurCents)}</td>
        <td className="py-1.5 pr-3 text-right font-mono">{formatEURCents(month.spendingEurCents)}</td>
        <td className="py-1.5 text-right font-mono">{formatEURCents(month.netEurCents, { signed: true })}</td>
      </tr>)}</tbody>
    </table>
  </div>;
}

// CategoryBars is a ranked bar list. Each line names its category, amount
// and share directly, so the list doubles as its own table.
export function CategoryBars({ categories, spending, href }: {
  categories: CategoryLine[]; spending: number; href: (categoryId: number) => string;
}) {
  const largest = Math.max(1, ...categories.map((line) => line.spendingEurCents));
  const t = useTranslations("dashboard.charts");
  const percent = numberFormat({ style: "percent", maximumFractionDigits: 0 });
  return <ul className="flex flex-col py-1.5" aria-label={t("byCategory")}>
    {categories.map((line) => {
      const content = <>
        <span className="truncate text-muted-foreground group-hover/line:text-foreground">{line.categoryName}</span>
        <span aria-hidden="true" className="h-1.5 overflow-hidden rounded-full bg-muted">
          <span className={cn("block h-full rounded-full bg-spending", line.other && "opacity-60")}
            style={{ width: `${(Math.max(0, line.spendingEurCents) / largest) * 100}%` }} />
        </span>
        <span className="text-right font-mono text-[0.8125rem] font-medium">{formatEURCents(line.spendingEurCents)}</span>
        <span className="text-right font-mono text-[0.6875rem] text-faint max-sm:hidden">{spending > 0 ? percent.format(line.spendingEurCents / spending) : ""}</span>
      </>;
      const className = "group/line grid grid-cols-[6rem_1fr_5.5rem] items-center gap-2.5 px-3.5 py-1.5 sm:grid-cols-[6rem_1fr_5.5rem_2.25rem]";
      return <li key={line.other ? "other" : line.categoryId ?? line.categoryName}>
        {line.categoryId !== null && !line.other
          ? <Link href={href(line.categoryId)} className={cn(className, "hover:bg-muted/50 focus-visible:-outline-offset-2")}>
            {content}<span className="sr-only">{t("showTransactions")}</span>
          </Link>
          : <div className={className}>{content}</div>}
      </li>;
    })}
  </ul>;
}

export type CategorySlice = {
  key: string; name: string; cents: number; color: string; categoryId: number | null; other: boolean;
};

// wedge draws one ring segment clockwise from the top of the circle.
function wedge(center: number, outer: number, inner: number, start: number, end: number) {
  const point = (radius: number, angle: number) => `${center + radius * Math.sin(angle)},${center - radius * Math.cos(angle)}`;
  const large = end - start > Math.PI ? 1 : 0;
  return `M${point(outer, start)}A${outer},${outer} 0 ${large} 1 ${point(outer, end)}`
    + `L${point(inner, end)}A${inner},${inner} 0 ${large} 0 ${point(inner, start)}Z`;
}

// CategoryPie is a donut of spending shares. The center reads the total, or
// the slice under the pointer or focus. The legend beside it names every
// slice with its amount and share, so it doubles as the table view.
export function CategoryPie({ slices, label, href }: {
  slices: CategorySlice[]; label: string; href: (categoryId: number) => string;
}) {
  const t = useTranslations("dashboard.charts");
  const [active, setActive] = useState<string | null>(null);
  const percent = numberFormat({ style: "percent", maximumFractionDigits: 1 });
  const total = slices.reduce((sum, slice) => sum + slice.cents, 0);
  const size = 184;
  const center = size / 2;
  const outer = center - 6;
  const inner = outer - 26;
  const current = slices.find((slice) => slice.key === active);
  const arcs = slices.reduce<{ slice: CategorySlice; start: number; end: number }[]>((list, slice) => {
    const start = list.length ? list[list.length - 1].end : 0;
    return [...list, { slice, start, end: start + (slice.cents / total) * Math.PI * 2 }];
  }, []);

  return <div className="grid items-center gap-4 p-3.5 sm:grid-cols-[auto_minmax(0,1fr)]" onPointerLeave={() => setActive(null)}>
    <div className="relative mx-auto" style={{ width: size, height: size }}>
      <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} aria-hidden="true" className="block overflow-visible">
        {arcs.map(({ slice, start, end }) => {
          const lifted = slice.key === active;
          const style = { fill: slice.color, opacity: active && !lifted ? 0.35 : 1 };
          return slices.length === 1
            ? <circle key={slice.key} cx={center} cy={center} r={(outer + inner) / 2} fill="none"
              style={{ stroke: slice.color }} strokeWidth={outer - inner} onPointerEnter={() => setActive(slice.key)} />
            : <path key={slice.key} d={wedge(center, lifted ? outer + 4 : outer, inner, start, end)} style={style}
              className="stroke-card transition-opacity" strokeWidth={2} strokeLinejoin="round"
              onPointerEnter={() => setActive(slice.key)} />;
        })}
      </svg>
      <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center px-12 text-center">
        <span className="max-w-full truncate text-xs text-muted-foreground">{current ? current.name : t("spent")}</span>
        <span className="font-mono text-[0.9375rem] font-medium tracking-tight">{formatEURCents(current ? current.cents : total)}</span>
        <span className="font-mono text-[0.6875rem] text-faint">{current ? percent.format(current.cents / total) : label}</span>
      </div>
    </div>
    <ul className="flex flex-col" aria-label={t("byCategoryIn", { period: label })}>
      {slices.map((slice) => {
        const content = <>
          <span aria-hidden="true" className="size-2.5 rounded-[2px]" style={{ background: slice.color }} />
          <span className="truncate text-muted-foreground group-hover/line:text-foreground">{slice.name}</span>
          <span className="text-right font-mono text-[0.8125rem] font-medium">{formatEURCents(slice.cents)}</span>
          <span className="text-right font-mono text-[0.6875rem] text-faint">{percent.format(slice.cents / total)}</span>
        </>;
        const className = cn("group/line grid grid-cols-[0.625rem_minmax(0,1fr)_auto_3rem] items-center gap-2.5 rounded-md px-2 py-1.5",
          slice.key === active && "bg-muted/60");
        const events = { onPointerEnter: () => setActive(slice.key), onFocus: () => setActive(slice.key), onBlur: () => setActive(null) };
        return <li key={slice.key}>
          {slice.categoryId !== null && !slice.other
            ? <Link href={href(slice.categoryId)} className={cn(className, "hover:bg-muted/50 focus-visible:-outline-offset-2")} {...events}>
              {content}<span className="sr-only">{t("showTransactions")}</span>
            </Link>
            : <div className={className} {...events}>{content}</div>}
        </li>;
      })}
    </ul>
  </div>;
}
